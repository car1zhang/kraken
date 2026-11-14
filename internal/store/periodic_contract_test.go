package store

// periodic_contract_test.go — athena-logic's BLIND tests for 50-contract.md
// (kraken-periodic-jobs), Slice 1: the periodic_jobs store accessors and
// schema v42. Written from the frozen contract alone, before the
// implementation existed; internal/store/periodic.go and the working-tree
// schema.go were never opened. Dimension: LOGIC (stated invariants and happy
// paths). Refusals (ErrPeriodicTaken, ErrNotFound) are athena-edge's.
//
// Every package-level identifier here starts with periodicContract or
// TestPeriodicContract so the edge author's file in this package cannot
// collide with it.

import (
	"reflect"
	"testing"
)

// periodicContractRow builds a fully-populated row, Last* included, so a test
// can prove which fields a write did and did not take.
func periodicContractRow(name, prefix string) *PeriodicJob {
	return &PeriodicJob{
		Name:      name,
		Prefix:    prefix,
		Template:  "---\nrepo: kraken\n---\nbody for {{slug}} on {{date}}\n",
		AtMinute:  9*60 + 30,
		Days:      1<<1 | 1<<3 | 1<<5,
		Enabled:   true,
		ArmedAt:   1769000000000,
		LastDate:  "2026-01-01",
		LastSlug:  "carried-slug",
		LastNote:  "carried-note",
		LastAt:    1768000000000,
		CreatedAt: 1769000000001,
	}
}

func periodicContractCreate(t *testing.T, s *Store, p *PeriodicJob) int64 {
	t.Helper()
	id, err := s.CreatePeriodicJob(p)
	if err != nil {
		t.Fatalf("CreatePeriodicJob(%q): %v", p.Name, err)
	}
	return id
}

func periodicContractGet(t *testing.T, s *Store, name string) *PeriodicJob {
	t.Helper()
	p, err := s.GetPeriodicJob(name)
	if err != nil {
		t.Fatalf("GetPeriodicJob(%q): %v", name, err)
	}
	return p
}

// Contract, schemaVersion: "A fresh database reports PRAGMA user_version = 42
// and can store periodic rows."
func TestPeriodicContractFreshSchemaIsV42(t *testing.T) {
	s := openTest(t)
	var version int
	if err := s.DB().QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != 42 {
		t.Fatalf("user_version = %d, want 42", version)
	}
	if schemaVersion != 42 {
		t.Fatalf("schemaVersion = %d, want 42", schemaVersion)
	}
	periodicContractCreate(t, s, periodicContractRow("standup", "standup"))
	rows, err := s.ListPeriodicJobs()
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListPeriodicJobs = %d rows, err %v; want 1 row", len(rows), err)
	}
}

// Contract, schemaVersion: "A database at version 41 opens, migrates to 42 and
// keeps its existing job rows."
//
// The contract does not name the periodic table, so the v41 fixture is built
// with the package's own downgradeFreshDB helper (reported as a silence).
func TestPeriodicContractMigrateV41KeepsJobs(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "legacy", Repo: "kraken", Body: "b", State: "blocked"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	downgradeFreshDB(t, s, 41)
	if _, err := s.db.Exec(`PRAGMA user_version = 41`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(); err != nil {
		t.Fatalf("migrate v41 → v42: %v", err)
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 42 {
		t.Fatalf("user_version after migrate = %d, want 42", version)
	}
	j, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID after migrate: %v", err)
	}
	if j.Slug != "legacy" || j.State != "blocked" {
		t.Fatalf("migrated job = %q/%q, want legacy/blocked", j.Slug, j.State)
	}
	periodicContractCreate(t, s, periodicContractRow("after-migrate", "after-migrate"))
}

// Contract, CreatePeriodicJob: "Stores Name, Prefix, Template, AtMinute, Days,
// Enabled, ArmedAt, CreatedAt as given and returns the new row's id. The four
// Last* fields start empty or zero whatever the struct carries."
func TestPeriodicContractCreateRoundTrip(t *testing.T) {
	s := openTest(t)
	in := periodicContractRow("standup notes", "standup")
	id := periodicContractCreate(t, s, in)
	got := periodicContractGet(t, s, "standup notes")

	want := PeriodicJob{
		ID:        id,
		Name:      in.Name,
		Prefix:    in.Prefix,
		Template:  in.Template,
		AtMinute:  in.AtMinute,
		Days:      in.Days,
		Enabled:   true,
		ArmedAt:   in.ArmedAt,
		CreatedAt: in.CreatedAt,
	}
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("stored row = %+v\nwant         %+v", *got, want)
	}
}

// Contract, CreatePeriodicJob stores Enabled "as given": a disabled row stays
// disabled.
func TestPeriodicContractCreateStoresDisabled(t *testing.T) {
	s := openTest(t)
	in := periodicContractRow("off", "off")
	in.Enabled = false
	periodicContractCreate(t, s, in)
	if got := periodicContractGet(t, s, "off"); got.Enabled {
		t.Fatalf("Enabled = true, want false as given")
	}
}

// Contract, CreatePeriodicJob: "returns the new row's id" — two rows, two
// distinct ids, each matching what GetPeriodicJob reads back.
func TestPeriodicContractCreateReturnsRowID(t *testing.T) {
	s := openTest(t)
	a := periodicContractCreate(t, s, periodicContractRow("a", "a"))
	b := periodicContractCreate(t, s, periodicContractRow("b", "b"))
	if a == b {
		t.Fatalf("two rows share id %d", a)
	}
	if got := periodicContractGet(t, s, "a").ID; got != a {
		t.Fatalf("row a ID = %d, want %d", got, a)
	}
	if got := periodicContractGet(t, s, "b").ID; got != b {
		t.Fatalf("row b ID = %d, want %d", got, b)
	}
}

// Contract, ListPeriodicJobs: "No rows is a zero-length result with a nil
// error."
func TestPeriodicContractListEmpty(t *testing.T) {
	s := openTest(t)
	rows, err := s.ListPeriodicJobs()
	if err != nil {
		t.Fatalf("ListPeriodicJobs: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("ListPeriodicJobs = %d rows, want 0", len(rows))
	}
}

// Contract, ListPeriodicJobs: "Every row, in creation order." Names are chosen
// so creation order is neither alphabetical nor reverse-alphabetical.
func TestPeriodicContractListCreationOrder(t *testing.T) {
	s := openTest(t)
	for _, n := range []string{"mike", "alpha", "zulu", "bravo"} {
		periodicContractCreate(t, s, periodicContractRow(n, n))
	}
	rows, err := s.ListPeriodicJobs()
	if err != nil {
		t.Fatalf("ListPeriodicJobs: %v", err)
	}
	var names []string
	for _, r := range rows {
		names = append(names, r.Name)
	}
	if want := []string{"mike", "alpha", "zulu", "bravo"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("order = %v, want %v", names, want)
	}
}

// Contract, GetPeriodicJob: "The row whose Name equals name exactly."
func TestPeriodicContractGetByExactName(t *testing.T) {
	s := openTest(t)
	periodicContractCreate(t, s, periodicContractRow("Weekly Review", "weekly"))
	periodicContractCreate(t, s, periodicContractRow("Weekly", "weekly-two"))
	if got := periodicContractGet(t, s, "Weekly"); got.Prefix != "weekly-two" {
		t.Fatalf("GetPeriodicJob(Weekly).Prefix = %q, want weekly-two", got.Prefix)
	}
	if got := periodicContractGet(t, s, "Weekly Review"); got.Prefix != "weekly" {
		t.Fatalf("GetPeriodicJob(Weekly Review).Prefix = %q, want weekly", got.Prefix)
	}
}

// Contract, SetPeriodicEnabled: "enabled == false clears Enabled and leaves
// ArmedAt as it was ... enabled == true sets Enabled and sets ArmedAt to at.
// Neither changes any Last* field."
func TestPeriodicContractSetEnabled(t *testing.T) {
	s := openTest(t)
	id := periodicContractCreate(t, s, periodicContractRow("standup", "standup"))
	if err := s.StampPeriodicFiring(id, "2026-11-12", "standup-2026-11-12", "note", 1769100000000); err != nil {
		t.Fatalf("StampPeriodicFiring: %v", err)
	}

	if err := s.SetPeriodicEnabled("standup", false, 1769200000000); err != nil {
		t.Fatalf("SetPeriodicEnabled(false): %v", err)
	}
	got := periodicContractGet(t, s, "standup")
	if got.Enabled {
		t.Fatalf("after disable Enabled = true")
	}
	if got.ArmedAt != 1769000000000 {
		t.Fatalf("after disable ArmedAt = %d, want unchanged 1769000000000", got.ArmedAt)
	}
	if got.LastDate != "2026-11-12" || got.LastSlug != "standup-2026-11-12" || got.LastNote != "note" || got.LastAt != 1769100000000 {
		t.Fatalf("disable changed Last*: %+v", got)
	}

	if err := s.SetPeriodicEnabled("standup", true, 1769300000000); err != nil {
		t.Fatalf("SetPeriodicEnabled(true): %v", err)
	}
	got = periodicContractGet(t, s, "standup")
	if !got.Enabled {
		t.Fatalf("after enable Enabled = false")
	}
	if got.ArmedAt != 1769300000000 {
		t.Fatalf("after enable ArmedAt = %d, want 1769300000000", got.ArmedAt)
	}
	if got.LastDate != "2026-11-12" || got.LastSlug != "standup-2026-11-12" || got.LastNote != "note" || got.LastAt != 1769100000000 {
		t.Fatalf("enable changed Last*: %+v", got)
	}
}

// Contract, StampPeriodicFiring: "Writes LastDate, LastSlug, LastNote, LastAt
// and nothing else."
func TestPeriodicContractStampWritesOnlyLast(t *testing.T) {
	s := openTest(t)
	in := periodicContractRow("standup", "standup")
	id := periodicContractCreate(t, s, in)
	before := periodicContractGet(t, s, "standup")

	if err := s.StampPeriodicFiring(id, "2026-11-13", "standup-2026-11-13", "", 1769400000000); err != nil {
		t.Fatalf("StampPeriodicFiring: %v", err)
	}
	got := periodicContractGet(t, s, "standup")
	want := *before
	want.LastDate = "2026-11-13"
	want.LastSlug = "standup-2026-11-13"
	want.LastNote = ""
	want.LastAt = 1769400000000
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("after stamp = %+v\nwant          %+v", *got, want)
	}

	// A second stamp overwrites all four, including back to an empty slug.
	if err := s.StampPeriodicFiring(id, "2026-11-14", "", "standup-2026-11-14 is taken", 1769500000000); err != nil {
		t.Fatalf("second StampPeriodicFiring: %v", err)
	}
	got = periodicContractGet(t, s, "standup")
	want.LastDate = "2026-11-14"
	want.LastSlug = ""
	want.LastNote = "standup-2026-11-14 is taken"
	want.LastAt = 1769500000000
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("after second stamp = %+v\nwant                 %+v", *got, want)
	}
}

// Contract, StampPeriodicFiring writes the row with that id, not its siblings.
func TestPeriodicContractStampTargetsOneRow(t *testing.T) {
	s := openTest(t)
	periodicContractCreate(t, s, periodicContractRow("a", "a"))
	idB := periodicContractCreate(t, s, periodicContractRow("b", "b"))
	if err := s.StampPeriodicFiring(idB, "2026-11-13", "b-2026-11-13", "", 5); err != nil {
		t.Fatalf("StampPeriodicFiring: %v", err)
	}
	if a := periodicContractGet(t, s, "a"); a.LastDate != "" || a.LastSlug != "" || a.LastAt != 0 {
		t.Fatalf("row a was stamped: %+v", a)
	}
	if b := periodicContractGet(t, s, "b"); b.LastDate != "2026-11-13" {
		t.Fatalf("row b LastDate = %q, want 2026-11-13", b.LastDate)
	}
}

// Contract, DeletePeriodicJob: "Removes the row ... No job row is touched."
func TestPeriodicContractDeleteLeavesJobs(t *testing.T) {
	s := openTest(t)
	periodicContractCreate(t, s, periodicContractRow("standup", "standup"))
	periodicContractCreate(t, s, periodicContractRow("other", "other"))
	jobID, err := s.CreateJob(&Job{Slug: "standup-2026-11-13", Repo: "kraken", Body: "b", Author: "kraken"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	if err := s.DeletePeriodicJob("standup"); err != nil {
		t.Fatalf("DeletePeriodicJob: %v", err)
	}
	rows, err := s.ListPeriodicJobs()
	if err != nil {
		t.Fatalf("ListPeriodicJobs: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "other" {
		t.Fatalf("rows after delete = %+v, want only other", rows)
	}
	j, err := s.GetJobByID(jobID)
	if err != nil {
		t.Fatalf("job row gone after DeletePeriodicJob: %v", err)
	}
	if j.Slug != "standup-2026-11-13" || j.ArchivedAt != 0 {
		t.Fatalf("job row changed: slug %q archived_at %d", j.Slug, j.ArchivedAt)
	}
}
