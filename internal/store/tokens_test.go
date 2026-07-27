package store

import (
	"errors"
	"testing"
)

// TestGetJobTokensNotFoundIsMeaningful pins the distinction the wire shape
// depends on: a job that has never been measured is ErrNotFound, NOT a row of
// four zeros. Only that difference lets a consumer tell "no transcripts yet"
// from a genuinely cheap job.
func TestGetJobTokensNotFoundIsMeaningful(t *testing.T) {
	s := openTest(t)
	if _, err := s.GetJobTokens("never-ran"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetJobTokens on an unmeasured job = %v, want ErrNotFound", err)
	}
}

// TestBumpJobTokensInsertsThenAccumulates is this file's central contract, and
// it is two different rules in one statement: the four counters ADD (the
// caller passes a delta), while sessions is SET (it is a file count, and a
// caller that accumulated it would report a job with 3 transcripts as having
// dozens).
func TestBumpJobTokensInsertsThenAccumulates(t *testing.T) {
	s := openTest(t)
	if err := s.BumpJobTokens("demo", JobTokens{In: 10, Out: 100, CacheWrite: 1000, CacheRead: 10000, Sessions: 2}); err != nil {
		t.Fatalf("first bump: %v", err)
	}
	if err := s.BumpJobTokens("demo", JobTokens{In: 1, Out: 2, CacheWrite: 3, CacheRead: 4, Sessions: 3}); err != nil {
		t.Fatalf("second bump: %v", err)
	}
	got, err := s.GetJobTokens("demo")
	if err != nil {
		t.Fatalf("GetJobTokens: %v", err)
	}
	if got.In != 11 || got.Out != 102 || got.CacheWrite != 1003 || got.CacheRead != 10004 {
		t.Errorf("counters = %d/%d/%d/%d, want them summed", got.In, got.Out, got.CacheWrite, got.CacheRead)
	}
	if got.Sessions != 3 {
		t.Errorf("Sessions = %d, want 3 — a file count is SET, never accumulated", got.Sessions)
	}
	if got.Slug != "demo" || got.UpdatedAt == 0 {
		t.Errorf("row = %+v, want the slug back and a stamped updated_at", got)
	}
}

// TestBumpJobTokensAccumulatesTheZeusHalf pins that the split's four columns
// follow the totals' rule and not sessions': they are the same delta,
// partitioned, so they ADD. A SET here would make the row report only the
// latest pass's lead spend against an all-time total — a split that shrinks
// while the number it is a subset of grows.
func TestBumpJobTokensAccumulatesTheZeusHalf(t *testing.T) {
	s := openTest(t)
	if err := s.BumpJobTokens("demo", JobTokens{
		In: 10, Out: 100, CacheWrite: 1000, CacheRead: 10000, Sessions: 2,
		ZeusIn: 6, ZeusOut: 60, ZeusCacheWrite: 600, ZeusCacheRead: 6000,
	}); err != nil {
		t.Fatalf("first bump: %v", err)
	}
	if err := s.BumpJobTokens("demo", JobTokens{
		In: 1, Out: 2, CacheWrite: 3, CacheRead: 4, Sessions: 3,
		ZeusIn: 1, ZeusOut: 1, ZeusCacheWrite: 1, ZeusCacheRead: 1,
	}); err != nil {
		t.Fatalf("second bump: %v", err)
	}
	got, err := s.GetJobTokens("demo")
	if err != nil {
		t.Fatalf("GetJobTokens: %v", err)
	}
	if got.ZeusIn != 7 || got.ZeusOut != 61 || got.ZeusCacheWrite != 601 || got.ZeusCacheRead != 6001 {
		t.Errorf("zeus counters = %d/%d/%d/%d, want them summed",
			got.ZeusIn, got.ZeusOut, got.ZeusCacheWrite, got.ZeusCacheRead)
	}
	// ...and the invariant every consumer subtracts against: the zeus half is
	// a SUBSET of the total, so the derived subagent half is never negative.
	if got.ZeusIn > got.In || got.ZeusOut > got.Out ||
		got.ZeusCacheWrite > got.CacheWrite || got.ZeusCacheRead > got.CacheRead {
		t.Errorf("zeus half exceeds the total it is a subset of: %+v", got)
	}
}

// TestBumpJobTokensZeusAloneDoesNotStampUpdatedAt pins that the timestamp CASE
// still keys off the four TOTALS. The zeus columns are a subset, so the state
// this constructs — zeus spend with no total spend — is unreachable in
// practice; the test exists to pin that adding them to the condition was
// deliberately declined rather than forgotten.
func TestBumpJobTokensZeusAloneDoesNotStampUpdatedAt(t *testing.T) {
	s := openTest(t)
	if err := s.BumpJobTokens("demo", JobTokens{Out: 5, Sessions: 1, UpdatedAt: 1700000000000}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.BumpJobTokens("demo", JobTokens{ZeusOut: 9, Sessions: 2, UpdatedAt: 1800000000000}); err != nil {
		t.Fatalf("zeus-only bump: %v", err)
	}
	got, err := s.GetJobTokens("demo")
	if err != nil {
		t.Fatalf("GetJobTokens: %v", err)
	}
	if got.UpdatedAt != 1700000000000 {
		t.Errorf("UpdatedAt = %d, want the seed's — no TOTAL spend moved", got.UpdatedAt)
	}
	if got.ZeusOut != 9 {
		t.Errorf("ZeusOut = %d, want 9 — the column still accumulates", got.ZeusOut)
	}
}

// TestBumpJobTokensKeepsAnExplicitTimestamp is the other half of the stamping
// rule: a caller that supplies UpdatedAt (a test, an importer, a pass that
// already knows the tick's clock) gets it stored verbatim rather than
// overwritten by now.
func TestBumpJobTokensKeepsAnExplicitTimestamp(t *testing.T) {
	s := openTest(t)
	if err := s.BumpJobTokens("demo", JobTokens{Out: 1, Sessions: 1, UpdatedAt: 1700000000000}); err != nil {
		t.Fatalf("BumpJobTokens: %v", err)
	}
	got, err := s.GetJobTokens("demo")
	if err != nil {
		t.Fatalf("GetJobTokens: %v", err)
	}
	if got.UpdatedAt != 1700000000000 {
		t.Fatalf("UpdatedAt = %d, want the explicit value", got.UpdatedAt)
	}
}

// TestBumpJobTokensRejectsAnEmptySlug: the slug is the primary key, so a
// blank one would silently collect every unkeyed write into one row.
func TestBumpJobTokensRejectsAnEmptySlug(t *testing.T) {
	s := openTest(t)
	if err := s.BumpJobTokens("", JobTokens{Out: 1}); err == nil {
		t.Fatal("BumpJobTokens with an empty slug must be refused")
	}
	if err := s.SetJobTokenOffsets("", map[string]int64{"/a": 1}); err == nil {
		t.Fatal("SetJobTokenOffsets with an empty slug must be refused")
	}
}

// TestListJobTokensReturnsEveryMeasuredJob covers the bulk read the board
// build uses instead of one query per job.
func TestListJobTokensReturnsEveryMeasuredJob(t *testing.T) {
	s := openTest(t)
	for _, slug := range []string{"beta", "alpha"} {
		if err := s.BumpJobTokens(slug, JobTokens{Out: 5, Sessions: 1}); err != nil {
			t.Fatalf("bump %s: %v", slug, err)
		}
	}
	rows, err := s.ListJobTokens()
	if err != nil {
		t.Fatalf("ListJobTokens: %v", err)
	}
	if len(rows) != 2 || rows[0].Slug != "alpha" || rows[1].Slug != "beta" {
		t.Fatalf("rows = %+v, want both jobs slug-ordered", rows)
	}
}

// TestJobTokenOffsetsRoundTripAndReplace pins the watermark verbs: an
// unmeasured job reads as an empty (non-nil) map, a write round-trips, and a
// second write REPLACES rather than merges — a path missing from the new set
// is a transcript that is gone, and leaving its row behind would grow this
// table without bound.
func TestJobTokenOffsetsRoundTripAndReplace(t *testing.T) {
	s := openTest(t)
	empty, err := s.JobTokenOffsets("demo")
	if err != nil {
		t.Fatalf("JobTokenOffsets on an unmeasured job: %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("offsets = %v, want an empty non-nil map", empty)
	}

	if err := s.SetJobTokenOffsets("demo", map[string]int64{"/p/a.jsonl": 10, "/p/b.jsonl": 20}); err != nil {
		t.Fatalf("SetJobTokenOffsets: %v", err)
	}
	got, err := s.JobTokenOffsets("demo")
	if err != nil {
		t.Fatalf("JobTokenOffsets: %v", err)
	}
	if got["/p/a.jsonl"] != 10 || got["/p/b.jsonl"] != 20 || len(got) != 2 {
		t.Fatalf("offsets = %v", got)
	}

	// b.jsonl vanished, a.jsonl grew
	if err := s.SetJobTokenOffsets("demo", map[string]int64{"/p/a.jsonl": 99}); err != nil {
		t.Fatalf("SetJobTokenOffsets (replace): %v", err)
	}
	got, err = s.JobTokenOffsets("demo")
	if err != nil {
		t.Fatalf("JobTokenOffsets: %v", err)
	}
	if len(got) != 1 || got["/p/a.jsonl"] != 99 {
		t.Fatalf("offsets = %v, want the write to have REPLACED the set", got)
	}

	if err := s.SetJobTokenOffsets("demo", nil); err != nil {
		t.Fatalf("SetJobTokenOffsets (clear): %v", err)
	}
	got, _ = s.JobTokenOffsets("demo")
	if len(got) != 0 {
		t.Fatalf("offsets = %v, want an empty map after clearing", got)
	}
}

// TestAllJobTokenOffsetsIsScopedPerJob: the bulk read the daemon pass uses
// must not blend one job's watermarks into another's — two jobs can hold a
// watermark for the same absolute path only by accident, but a bug that
// flattened the key would make one job's offsets silently suppress the
// other's sum.
func TestAllJobTokenOffsetsIsScopedPerJob(t *testing.T) {
	s := openTest(t)
	if err := s.SetJobTokenOffsets("one", map[string]int64{"/p/a.jsonl": 1}); err != nil {
		t.Fatalf("set one: %v", err)
	}
	if err := s.SetJobTokenOffsets("two", map[string]int64{"/p/a.jsonl": 2, "/p/b.jsonl": 3}); err != nil {
		t.Fatalf("set two: %v", err)
	}
	all, err := s.AllJobTokenOffsets()
	if err != nil {
		t.Fatalf("AllJobTokenOffsets: %v", err)
	}
	if len(all) != 2 || all["one"]["/p/a.jsonl"] != 1 || all["two"]["/p/a.jsonl"] != 2 || all["two"]["/p/b.jsonl"] != 3 {
		t.Fatalf("all = %v", all)
	}
	if _, ok := all["nobody"]; ok {
		t.Fatal("an unmeasured job must simply be absent from the outer map")
	}
}

// TestJobTokensSurviveTheJobsCascade is the hard rule of this feature:
// retrospective cost is the point, so a token row must NOT be attached to the
// jobs table's ON DELETE CASCADE. It outlives archiving (which does not delete
// the job row at all) and it outlives a hard delete (which does).
func TestJobTokensSurviveTheJobsCascade(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "doomed", Repo: "kraken"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.BumpJobTokens("doomed", JobTokens{Out: 42, Sessions: 1}); err != nil {
		t.Fatalf("BumpJobTokens: %v", err)
	}
	if err := s.SetJobTokenOffsets("doomed", map[string]int64{"/p/a.jsonl": 7}); err != nil {
		t.Fatalf("SetJobTokenOffsets: %v", err)
	}
	if err := s.DeleteJob(id); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	got, err := s.GetJobTokens("doomed")
	if err != nil {
		t.Fatalf("the token row must survive a hard-deleted job: %v", err)
	}
	if got.Out != 42 {
		t.Fatalf("Out = %d, want 42", got.Out)
	}
	offs, err := s.JobTokenOffsets("doomed")
	if err != nil || offs["/p/a.jsonl"] != 7 {
		t.Fatalf("watermarks = %v (err %v), want them to survive too", offs, err)
	}
}
