package store

import (
	"errors"
	"testing"
)

// jobfreeze_test.go — schema v41's frozen_at: SetJobFrozen, which stamps and
// clears it, ThawJobFrozenAt, the daemon's conditional clear, and the thaw
// updateJobFieldsTx performs when a state write leaves "blocked".

func freezeTestJob(t *testing.T, s *Store, slug, state string) int64 {
	t.Helper()
	id, err := s.CreateJob(&Job{Slug: slug, Repo: "kraken", Body: "b", State: state, UpdatedAt: 1700000000000})
	if err != nil {
		t.Fatalf("CreateJob %s: %v", slug, err)
	}
	return id
}

func frozenAtOf(t *testing.T, s *Store, id int64) int64 {
	t.Helper()
	j, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID %d: %v", id, err)
	}
	return j.FrozenAt
}

// A new job is not frozen; freezing a blocked one stamps the column and the
// scan reads it back without shifting its neighbours (scanJob is positional).
func TestSetJobFrozenRoundTrip(t *testing.T) {
	s := openTest(t)
	id := freezeTestJob(t, s, "hold", "blocked")
	if got := frozenAtOf(t, s, id); got != 0 {
		t.Fatalf("fresh job FrozenAt = %d, want 0", got)
	}

	if err := s.SetJobFrozen(id, 1769040000000); err != nil {
		t.Fatalf("SetJobFrozen: %v", err)
	}
	got, err := s.GetJobBySlug("hold")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if got.FrozenAt != 1769040000000 {
		t.Fatalf("FrozenAt = %d, want 1769040000000", got.FrozenAt)
	}
	if got.Slug != "hold" || got.Repo != "kraken" || got.State != "blocked" || got.Author != "unknown" || got.Visibility != "readonly" {
		t.Fatalf("columns shifted around frozen_at: %+v", got)
	}

	// the list path scans the same columns
	all, err := s.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(all) != 1 || all[0].FrozenAt != 1769040000000 {
		t.Fatalf("ListJobs = %+v, want one job carrying the stamp", all)
	}
}

// A hold bumps neither rev nor updated_at: the card's age must survive it,
// and it is not a field the editor's optimistic lock covers.
func TestSetJobFrozenLeavesRevAndUpdatedAt(t *testing.T) {
	s := openTest(t)
	id := freezeTestJob(t, s, "quiet-hold", "blocked")
	before, err := s.GetJobByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetJobFrozen(id, 5); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if err := s.SetJobFrozen(id, 0); err != nil {
		t.Fatalf("thaw: %v", err)
	}
	after, err := s.GetJobByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Rev != before.Rev || after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("rev %d→%d, updated_at %d→%d: a hold must move neither",
			before.Rev, after.Rev, before.UpdatedAt, after.UpdatedAt)
	}
}

// Only a blocked job can be frozen, and the refusal is told apart from an
// unknown id.
func TestSetJobFrozenRefusesNonBlocked(t *testing.T) {
	s := openTest(t)
	for _, state := range []string{"", "working", "done"} {
		id := freezeTestJob(t, s, "st-"+state+"x", state)
		err := s.SetJobFrozen(id, 7)
		if !errors.Is(err, ErrJobNotBlocked) {
			t.Errorf("freeze a %q job: err = %v, want ErrJobNotBlocked", state, err)
		}
		if got := frozenAtOf(t, s, id); got != 0 {
			t.Errorf("refused freeze of a %q job left FrozenAt = %d", state, got)
		}
	}
	if err := s.SetJobFrozen(987654, 7); !errors.Is(err, ErrNotFound) {
		t.Errorf("freeze an unknown id: err = %v, want ErrNotFound", err)
	}
	if err := s.SetJobFrozen(987654, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("thaw an unknown id: err = %v, want ErrNotFound", err)
	}
}

// Thawing is idempotent and does not care about state.
func TestSetJobFrozenThawIsIdempotent(t *testing.T) {
	s := openTest(t)
	blocked := freezeTestJob(t, s, "thaw-blocked", "blocked")
	working := freezeTestJob(t, s, "thaw-working", "working")
	for _, id := range []int64{blocked, working} {
		if err := s.SetJobFrozen(id, 0); err != nil {
			t.Errorf("thaw of a job that is not frozen (id %d): %v", id, err)
		}
	}
	if err := s.SetJobFrozen(blocked, 9); err != nil {
		t.Fatal(err)
	}
	if err := s.SetJobFrozen(blocked, 0); err != nil {
		t.Fatalf("thaw: %v", err)
	}
	if got := frozenAtOf(t, s, blocked); got != 0 {
		t.Fatalf("after thaw FrozenAt = %d, want 0", got)
	}
}

// The daemon's thaw clears only the hold it judged: a hold placed since (a
// different stamp) is left standing, and the call says which happened.
func TestThawJobFrozenAtIsConditionalOnTheStamp(t *testing.T) {
	s := openTest(t)
	id := freezeTestJob(t, s, "judged", "blocked")
	if err := s.SetJobFrozen(id, 100); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetJobByID(id)
	if err != nil {
		t.Fatal(err)
	}

	// the operator took it off hold and put it back on: the stamp moved
	if err := s.SetJobFrozen(id, 200); err != nil {
		t.Fatal(err)
	}
	cleared, err := s.ThawJobFrozenAt(id, 100)
	if err != nil || cleared {
		t.Fatalf("thaw against a superseded stamp = (%v, %v), want (false, nil)", cleared, err)
	}
	if got := frozenAtOf(t, s, id); got != 200 {
		t.Fatalf("the newer hold was disturbed: FrozenAt = %d, want 200", got)
	}

	cleared, err = s.ThawJobFrozenAt(id, 200)
	if err != nil || !cleared {
		t.Fatalf("thaw against the current stamp = (%v, %v), want (true, nil)", cleared, err)
	}
	if got := frozenAtOf(t, s, id); got != 0 {
		t.Fatalf("FrozenAt after the thaw = %d, want 0", got)
	}

	// already gone, a zero stamp, and an unknown id all clear nothing
	for name, call := range map[string]func() (bool, error){
		"already thawed": func() (bool, error) { return s.ThawJobFrozenAt(id, 200) },
		"zero stamp":     func() (bool, error) { return s.ThawJobFrozenAt(id, 0) },
		"unknown id":     func() (bool, error) { return s.ThawJobFrozenAt(987654, 200) },
	} {
		if cleared, err := call(); err != nil || cleared {
			t.Errorf("%s = (%v, %v), want (false, nil)", name, cleared, err)
		}
	}

	after, err := s.GetJobByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Rev != before.Rev || after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("rev %d→%d, updated_at %d→%d: the thaw must move neither",
			before.Rev, after.Rev, before.UpdatedAt, after.UpdatedAt)
	}
}

// A zero stamp must never match an unfrozen row and report a thaw.
func TestThawJobFrozenAtZeroStampOnAnUnfrozenRow(t *testing.T) {
	s := openTest(t)
	id := freezeTestJob(t, s, "plain", "blocked")
	if cleared, err := s.ThawJobFrozenAt(id, 0); err != nil || cleared {
		t.Fatalf("zero stamp on an unfrozen row = (%v, %v), want (false, nil)", cleared, err)
	}
}

// An archived row is out of the active namespace and is never frozen.
func TestSetJobFrozenRefusesArchived(t *testing.T) {
	s := openTest(t)
	id := freezeTestJob(t, s, "gone", "blocked")
	if _, err := s.ArchiveJob(id); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	if err := s.SetJobFrozen(id, 7); !errors.Is(err, ErrNotFound) {
		t.Fatalf("freeze an archived job: err = %v, want ErrNotFound", err)
	}
}

// The thaw: any state write that leaves "blocked" clears the mark in the
// same UPDATE, and nothing else does.
func TestUpdateJobFieldsThawsOffBlocked(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields map[string]any
		want   int64 // FrozenAt afterwards, given a stamp of 11
	}{
		{"working clears", map[string]any{"state": "working"}, 0},
		{"done clears", map[string]any{"state": "done"}, 0},
		{"draft clears", map[string]any{"state": ""}, 0},
		{"working beside another field clears", map[string]any{"state": "working", "outcome": ""}, 0},
		{"blocked keeps", map[string]any{"state": "blocked"}, 11},
		{"a non-state field keeps", map[string]any{"session": "abc"}, 11},
		{"color keeps", map[string]any{"color": "purple"}, 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openTest(t)
			id := freezeTestJob(t, s, "thaw", "blocked")
			if err := s.SetJobFrozen(id, 11); err != nil {
				t.Fatal(err)
			}
			j, err := s.GetJobByID(id)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.UpdateJobFields(id, j.Rev, tc.fields); err != nil {
				t.Fatalf("UpdateJobFields(%v): %v", tc.fields, err)
			}
			if got := frozenAtOf(t, s, id); got != tc.want {
				t.Fatalf("FrozenAt after %v = %d, want %d", tc.fields, got, tc.want)
			}
		})
	}
}

// A thawed job that blocks again is an ordinary blocked job: the mark does
// not come back by itself.
func TestFrozenDoesNotReturnOnReblock(t *testing.T) {
	s := openTest(t)
	id := freezeTestJob(t, s, "cycle", "blocked")
	if err := s.SetJobFrozen(id, 11); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"working", "blocked"} {
		j, err := s.GetJobByID(id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.UpdateJobFields(id, j.Rev, map[string]any{"state": state}); err != nil {
			t.Fatalf("state → %s: %v", state, err)
		}
	}
	if got := frozenAtOf(t, s, id); got != 0 {
		t.Fatalf("FrozenAt after working → blocked = %d, want 0", got)
	}
}

// Archive and unarchive preserve the mark with the state it belongs to.
func TestFrozenSurvivesArchiveRoundTrip(t *testing.T) {
	s := openTest(t)
	id := freezeTestJob(t, s, "shelf", "blocked")
	if err := s.SetJobFrozen(id, 11); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveJob(id); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	if err := s.UnarchiveJob(id); err != nil {
		t.Fatalf("UnarchiveJob: %v", err)
	}
	j, err := s.GetJobByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if j.State != "blocked" || j.FrozenAt != 11 {
		t.Fatalf("after archive round trip: state %q FrozenAt %d, want blocked/11", j.State, j.FrozenAt)
	}
}

// A v40 database gains the column with every existing row unfrozen, and the
// step replays as a no-op over a table that already has it.
func TestMigrateV41AddsFrozenAt(t *testing.T) {
	s := openTest(t)
	id := freezeTestJob(t, s, "legacy", "blocked")
	downgradeFreshDB(t, s, 40)
	if _, err := s.db.Exec(`PRAGMA user_version = 40`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('jobs') WHERE name = 'frozen_at'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("downgrade left jobs.frozen_at in place; the fixture is not a v40 shape")
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate v40 → v41: %v", err)
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || schemaVersion < 41 {
		t.Fatalf("user_version = %d, schemaVersion = %d, want both at least 41 and equal", version, schemaVersion)
	}
	j, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID after migration: %v", err)
	}
	if j.Slug != "legacy" || j.State != "blocked" || j.FrozenAt != 0 {
		t.Fatalf("migrated row = slug %q state %q FrozenAt %d, want legacy/blocked/0", j.Slug, j.State, j.FrozenAt)
	}

	// replay: version reset alone, column still present
	if err := s.SetJobFrozen(id, 11); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 40`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrate(); err != nil {
		t.Fatalf("replay of v41 over an existing column: %v", err)
	}
	if got := frozenAtOf(t, s, id); got != 11 {
		t.Fatalf("replay changed FrozenAt to %d, want 11 kept", got)
	}
}
