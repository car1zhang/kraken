package store

import (
	"errors"
	"path/filepath"
	"testing"
)

// periodic_edge_test.go — athena-edge's blind suite for slice 1 of the
// periodic-jobs contract (50-contract.md, "Slice 1: store"): the refusals
// (ErrPeriodicTaken, ErrNotFound), what each writer must leave alone, and the
// boundaries of the exact-name lookup. Written from the contract only; the
// happy paths belong to athena-logic.

// periodicEdgeStore opens a scratch store under a scratch HOME.
func periodicEdgeStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	s, err := Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// periodicEdgeRow is a fully populated row to create from.
func periodicEdgeRow(name, prefix string) *PeriodicJob {
	return &PeriodicJob{
		Name:      name,
		Prefix:    prefix,
		Template:  "---\nrepo: kraken\n---\nbody {{date}}\n",
		AtMinute:  540,
		Days:      0,
		Enabled:   true,
		ArmedAt:   1_700_000_000_000,
		CreatedAt: 1_700_000_000_000,
	}
}

func periodicEdgeCreate(t *testing.T, s *Store, p *PeriodicJob) int64 {
	t.Helper()
	id, err := s.CreatePeriodicJob(p)
	if err != nil {
		t.Fatalf("CreatePeriodicJob(%q): %v", p.Name, err)
	}
	return id
}

func periodicEdgeList(t *testing.T, s *Store) []PeriodicJob {
	t.Helper()
	rows, err := s.ListPeriodicJobs()
	if err != nil {
		t.Fatalf("ListPeriodicJobs: %v", err)
	}
	return rows
}

func periodicEdgeGet(t *testing.T, s *Store, name string) *PeriodicJob {
	t.Helper()
	p, err := s.GetPeriodicJob(name)
	if err != nil {
		t.Fatalf("GetPeriodicJob(%q): %v", name, err)
	}
	return p
}

// Contract: "A `Name` equal to an existing row's ... returns an error for which
// `errors.Is(err, ErrPeriodicTaken)` holds, and inserts nothing."
func TestPeriodicEdgeCreateDuplicateNameRefused(t *testing.T) {
	s := periodicEdgeStore(t)
	periodicEdgeCreate(t, s, periodicEdgeRow("nightly", "nightly"))
	_, err := s.CreatePeriodicJob(periodicEdgeRow("nightly", "other-prefix"))
	if !errors.Is(err, ErrPeriodicTaken) {
		t.Fatalf("duplicate name: err = %v, want ErrPeriodicTaken", err)
	}
	if rows := periodicEdgeList(t, s); len(rows) != 1 {
		t.Fatalf("rows after refused create = %d, want 1 (inserts nothing)", len(rows))
	}
}

// Contract: "... or a `Prefix` equal to an existing row's, returns an error for
// which `errors.Is(err, ErrPeriodicTaken)` holds, and inserts nothing."
func TestPeriodicEdgeCreateDuplicatePrefixRefused(t *testing.T) {
	s := periodicEdgeStore(t)
	periodicEdgeCreate(t, s, periodicEdgeRow("nightly", "nightly"))
	_, err := s.CreatePeriodicJob(periodicEdgeRow("a-different-name", "nightly"))
	if !errors.Is(err, ErrPeriodicTaken) {
		t.Fatalf("duplicate prefix: err = %v, want ErrPeriodicTaken", err)
	}
	if rows := periodicEdgeList(t, s); len(rows) != 1 {
		t.Fatalf("rows after refused create = %d, want 1 (inserts nothing)", len(rows))
	}
	if _, err := s.GetPeriodicJob("a-different-name"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refused row is readable: err = %v, want ErrNotFound", err)
	}
}

// Contract: "The four `Last*` fields start empty or zero whatever the struct
// carries."
func TestPeriodicEdgeCreateIgnoresGivenLastFields(t *testing.T) {
	s := periodicEdgeStore(t)
	p := periodicEdgeRow("nightly", "nightly")
	p.LastDate = "2026-11-12"
	p.LastSlug = "nightly-2026-11-12"
	p.LastNote = "something"
	p.LastAt = 1_700_000_123_456
	periodicEdgeCreate(t, s, p)
	got := periodicEdgeGet(t, s, "nightly")
	if got.LastDate != "" || got.LastSlug != "" || got.LastNote != "" || got.LastAt != 0 {
		t.Fatalf("Last* = %q %q %q %d, want all empty/zero", got.LastDate, got.LastSlug, got.LastNote, got.LastAt)
	}
}

// Contract: "Stores ... `Enabled` ... as given": a row created disabled reads
// back disabled.
func TestPeriodicEdgeCreateDisabledStaysDisabled(t *testing.T) {
	s := periodicEdgeStore(t)
	p := periodicEdgeRow("nightly", "nightly")
	p.Enabled = false
	periodicEdgeCreate(t, s, p)
	if got := periodicEdgeGet(t, s, "nightly"); got.Enabled {
		t.Fatal("Enabled = true, want false as given")
	}
}

// Contract: "Stores ... `AtMinute`, `Days` ... as given" at the extremes of
// their ranges (0..1439, the full seven-day mask).
func TestPeriodicEdgeCreateStoresRangeExtremes(t *testing.T) {
	s := periodicEdgeStore(t)
	lo := periodicEdgeRow("lo", "lo")
	lo.AtMinute, lo.Days = 0, 1 // Sunday only, bit 0
	hi := periodicEdgeRow("hi", "hi")
	hi.AtMinute, hi.Days = 1439, 127
	periodicEdgeCreate(t, s, lo)
	periodicEdgeCreate(t, s, hi)
	if g := periodicEdgeGet(t, s, "lo"); g.AtMinute != 0 || g.Days != 1 {
		t.Errorf("lo = AtMinute %d Days %d, want 0 1", g.AtMinute, g.Days)
	}
	if g := periodicEdgeGet(t, s, "hi"); g.AtMinute != 1439 || g.Days != 127 {
		t.Errorf("hi = AtMinute %d Days %d, want 1439 127", g.AtMinute, g.Days)
	}
}

// Contract: "No rows is a zero-length result with a nil error."
func TestPeriodicEdgeListEmpty(t *testing.T) {
	s := periodicEdgeStore(t)
	rows, err := s.ListPeriodicJobs()
	if err != nil {
		t.Fatalf("ListPeriodicJobs on empty store: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
}

// Contract: "The row whose `Name` equals `name` exactly. No such row:
// `ErrNotFound`." A case variant, a padded variant, a prefix of the name and
// the row's prefix field are all not equal.
func TestPeriodicEdgeGetExactNameOnly(t *testing.T) {
	s := periodicEdgeStore(t)
	periodicEdgeCreate(t, s, periodicEdgeRow("Nightly Notes", "notes"))
	for _, name := range []string{"", "nightly notes", "NIGHTLY NOTES", " Nightly Notes", "Nightly Notes ", "Nightly", "notes"} {
		if _, err := s.GetPeriodicJob(name); !errors.Is(err, ErrNotFound) {
			t.Errorf("GetPeriodicJob(%q): err = %v, want ErrNotFound", name, err)
		}
	}
	if got := periodicEdgeGet(t, s, "Nightly Notes"); got.Name != "Nightly Notes" {
		t.Fatalf("exact lookup Name = %q", got.Name)
	}
}

// Contract: "No such row: `ErrNotFound`" for SetPeriodicEnabled, both
// directions, and nothing is created.
func TestPeriodicEdgeSetEnabledMissing(t *testing.T) {
	s := periodicEdgeStore(t)
	for _, on := range []bool{true, false} {
		if err := s.SetPeriodicEnabled("ghost", on, 42); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetPeriodicEnabled(ghost, %v): err = %v, want ErrNotFound", on, err)
		}
	}
	if rows := periodicEdgeList(t, s); len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
}

// Contract: "`enabled == false` clears `Enabled` and leaves `ArmedAt` as it
// was" — including on a row that is already disabled, and whatever `at` says.
func TestPeriodicEdgeDisableTwiceKeepsArmedAt(t *testing.T) {
	s := periodicEdgeStore(t)
	periodicEdgeCreate(t, s, periodicEdgeRow("nightly", "nightly"))
	for i, at := range []int64{9_999_999_999_999, 1} {
		if err := s.SetPeriodicEnabled("nightly", false, at); err != nil {
			t.Fatalf("disable #%d: %v", i, err)
		}
		got := periodicEdgeGet(t, s, "nightly")
		if got.Enabled {
			t.Fatalf("disable #%d: still enabled", i)
		}
		if got.ArmedAt != 1_700_000_000_000 {
			t.Fatalf("disable #%d: ArmedAt = %d, want the original 1700000000000", i, got.ArmedAt)
		}
	}
}

// Contract: "`enabled == true` sets `Enabled` and sets `ArmedAt` to `at`" —
// unconditionally, so enabling an already-enabled row re-arms it, even to an
// earlier time.
func TestPeriodicEdgeEnableAlreadyEnabledRearms(t *testing.T) {
	s := periodicEdgeStore(t)
	periodicEdgeCreate(t, s, periodicEdgeRow("nightly", "nightly"))
	for _, at := range []int64{1_800_000_000_000, 5} {
		if err := s.SetPeriodicEnabled("nightly", true, at); err != nil {
			t.Fatalf("enable at %d: %v", at, err)
		}
		got := periodicEdgeGet(t, s, "nightly")
		if !got.Enabled || got.ArmedAt != at {
			t.Fatalf("after enable at %d: Enabled %v ArmedAt %d", at, got.Enabled, got.ArmedAt)
		}
	}
}

// Contract: "Neither changes any `Last*` field."
func TestPeriodicEdgeSetEnabledKeepsLastFields(t *testing.T) {
	s := periodicEdgeStore(t)
	id := periodicEdgeCreate(t, s, periodicEdgeRow("nightly", "nightly"))
	if err := s.StampPeriodicFiring(id, "2026-11-12", "nightly-2026-11-12", "start refused", 1_700_000_500_000); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	for _, on := range []bool{false, true} {
		if err := s.SetPeriodicEnabled("nightly", on, 1_800_000_000_000); err != nil {
			t.Fatalf("SetPeriodicEnabled(%v): %v", on, err)
		}
		got := periodicEdgeGet(t, s, "nightly")
		if got.LastDate != "2026-11-12" || got.LastSlug != "nightly-2026-11-12" || got.LastNote != "start refused" || got.LastAt != 1_700_000_500_000 {
			t.Fatalf("after SetPeriodicEnabled(%v): Last* = %q %q %q %d", on, got.LastDate, got.LastSlug, got.LastNote, got.LastAt)
		}
	}
}

// Contract: StampPeriodicFiring — "No row with that id: `ErrNotFound`." Zero,
// negative and a never-issued id all miss.
func TestPeriodicEdgeStampMissingID(t *testing.T) {
	s := periodicEdgeStore(t)
	id := periodicEdgeCreate(t, s, periodicEdgeRow("nightly", "nightly"))
	for _, bad := range []int64{0, -1, id + 1000} {
		if err := s.StampPeriodicFiring(bad, "2026-11-13", "x", "", 1); !errors.Is(err, ErrNotFound) {
			t.Errorf("StampPeriodicFiring(%d): err = %v, want ErrNotFound", bad, err)
		}
	}
	got := periodicEdgeGet(t, s, "nightly")
	if got.LastDate != "" || got.LastSlug != "" || got.LastNote != "" || got.LastAt != 0 {
		t.Fatalf("a missed stamp wrote the real row: %+v", got)
	}
}

// Contract: StampPeriodicFiring "Writes `LastDate`, `LastSlug`, `LastNote`,
// `LastAt` and nothing else." A second stamp of empty values overwrites the
// first (it writes, it does not merge), and no other field moves.
func TestPeriodicEdgeStampWritesOnlyLastFields(t *testing.T) {
	s := periodicEdgeStore(t)
	p := periodicEdgeRow("nightly", "nightly")
	p.AtMinute, p.Days, p.Enabled = 61, 42, false
	id := periodicEdgeCreate(t, s, p)
	before := periodicEdgeGet(t, s, "nightly")

	if err := s.StampPeriodicFiring(id, "2026-11-13", "nightly-2026-11-13", "a note", 1_763_000_000_000); err != nil {
		t.Fatalf("stamp 1: %v", err)
	}
	if err := s.StampPeriodicFiring(id, "2026-11-14", "", "", 1_763_100_000_000); err != nil {
		t.Fatalf("stamp 2: %v", err)
	}
	got := periodicEdgeGet(t, s, "nightly")
	if got.LastDate != "2026-11-14" || got.LastSlug != "" || got.LastNote != "" || got.LastAt != 1_763_100_000_000 {
		t.Fatalf("Last* after second stamp = %q %q %q %d", got.LastDate, got.LastSlug, got.LastNote, got.LastAt)
	}
	if got.ID != before.ID || got.Name != before.Name || got.Prefix != before.Prefix ||
		got.Template != before.Template || got.AtMinute != before.AtMinute || got.Days != before.Days ||
		got.Enabled != before.Enabled || got.ArmedAt != before.ArmedAt || got.CreatedAt != before.CreatedAt {
		t.Fatalf("stamp moved a non-Last field:\nbefore %+v\nafter  %+v", before, got)
	}
}

// Contract: StampPeriodicFiring writes only the row with that id; a sibling
// row is untouched.
func TestPeriodicEdgeStampTouchesOneRow(t *testing.T) {
	s := periodicEdgeStore(t)
	a := periodicEdgeCreate(t, s, periodicEdgeRow("a", "a"))
	periodicEdgeCreate(t, s, periodicEdgeRow("b", "b"))
	if err := s.StampPeriodicFiring(a, "2026-11-13", "a-2026-11-13", "", 7); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	if got := periodicEdgeGet(t, s, "b"); got.LastDate != "" || got.LastAt != 0 {
		t.Fatalf("sibling row stamped: %+v", got)
	}
}

// Contract: DeletePeriodicJob — "No such row: `ErrNotFound`", including a
// second delete of a row already removed.
func TestPeriodicEdgeDeleteMissing(t *testing.T) {
	s := periodicEdgeStore(t)
	if err := s.DeletePeriodicJob("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete ghost: err = %v, want ErrNotFound", err)
	}
	periodicEdgeCreate(t, s, periodicEdgeRow("nightly", "nightly"))
	if err := s.DeletePeriodicJob("nightly"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.DeletePeriodicJob("nightly"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetPeriodicJob("nightly"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted row still readable: err = %v", err)
	}
}

// Contract: DeletePeriodicJob "Removes the row" — only that row; the
// exact-name rule means a case variant removes nothing.
func TestPeriodicEdgeDeleteExactNameOnly(t *testing.T) {
	s := periodicEdgeStore(t)
	periodicEdgeCreate(t, s, periodicEdgeRow("nightly", "nightly"))
	periodicEdgeCreate(t, s, periodicEdgeRow("weekly", "weekly"))
	if err := s.DeletePeriodicJob("Nightly"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete Nightly: err = %v, want ErrNotFound", err)
	}
	if err := s.DeletePeriodicJob("nightly"); err != nil {
		t.Fatalf("delete nightly: %v", err)
	}
	rows := periodicEdgeList(t, s)
	if len(rows) != 1 || rows[0].Name != "weekly" {
		t.Fatalf("rows = %+v, want only weekly", rows)
	}
}

// Contract: DeletePeriodicJob — "No job row is touched." A job whose slug
// carries the row's prefix survives the delete.
func TestPeriodicEdgeDeleteLeavesJobs(t *testing.T) {
	s := periodicEdgeStore(t)
	periodicEdgeCreate(t, s, periodicEdgeRow("nightly", "nightly"))
	if _, err := s.CreateJob(&Job{Slug: "nightly-2026-11-13", Repo: "kraken", Body: "spec\n"}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.DeletePeriodicJob("nightly"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	all, err := s.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(all) != 1 || all[0].Slug != "nightly-2026-11-13" {
		t.Fatalf("jobs after delete = %+v, want the one fired job", all)
	}
}

// Contracts combined: ErrPeriodicTaken fires on a name or prefix "equal to an
// existing row's"; once DeletePeriodicJob "Removes the row" neither is held,
// so the same name and prefix create again.
func TestPeriodicEdgeDeleteFreesNameAndPrefix(t *testing.T) {
	s := periodicEdgeStore(t)
	periodicEdgeCreate(t, s, periodicEdgeRow("nightly", "nightly"))
	if err := s.DeletePeriodicJob("nightly"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.CreatePeriodicJob(periodicEdgeRow("nightly", "nightly")); err != nil {
		t.Fatalf("re-create after delete: %v", err)
	}
	if rows := periodicEdgeList(t, s); len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
}
