package store

import (
	"errors"
	"testing"
	"time"
)

// jobload_test.go — schema v13's four load_* columns and SetJobLoad, the one
// writer that touches them.

// TestSetJobLoadRoundTrip covers the two paths that can go wrong
// independently: scanJob's POSITIONAL scan (a column added to jobColumns but
// not scanJob, or vice versa, silently shifts every later field), and the
// UPDATE statement itself. Same shape as TestJobColorRoundTrip, which pins
// the colour column for the same reason.
func TestSetJobLoadRoundTrip(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "triage", Repo: "kraken", Body: "b", State: "blocked"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	// A brand-new job is "never estimated" — the state schemaV13's DEFAULTs
	// land every pre-existing row on, spelled the same way.
	got, err := s.GetJobBySlug("triage")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if (got.Load != JobLoad{}) {
		t.Fatalf("fresh job load = %+v, want the zero JobLoad", got.Load)
	}

	want := JobLoad{Unblock: 2, Scope: 8, Note: "one decision: which store keys the row", At: 1769040000000}
	if err := s.SetJobLoad(id, want); err != nil {
		t.Fatalf("SetJobLoad: %v", err)
	}
	got, err = s.GetJobBySlug("triage")
	if err != nil {
		t.Fatalf("GetJobBySlug after set: %v", err)
	}
	if got.Load != want {
		t.Fatalf("load = %+v, want %+v", got.Load, want)
	}
	// the neighbours of the new scan positions, asserted explicitly: a
	// positional slip shows up here rather than as a mystery elsewhere
	if got.Slug != "triage" || got.Repo != "kraken" || got.Body != "b" || got.State != "blocked" || got.Err != "" {
		t.Fatalf("columns shifted around load: %+v", got)
	}

	// clearing is a normal write of the zero pair, not a delete
	if err := s.SetJobLoad(id, JobLoad{}); err != nil {
		t.Fatalf("SetJobLoad clear: %v", err)
	}
	got, err = s.GetJobBySlug("triage")
	if err != nil {
		t.Fatalf("GetJobBySlug after clear: %v", err)
	}
	if (got.Load != JobLoad{}) {
		t.Fatalf("cleared load = %+v, want the zero JobLoad", got.Load)
	}
}

// TestSetJobLoadLeavesRevAndUpdatedAt is the whole reason this write is not
// an UpdateJobFields call: zeus rewrites the estimate every turn, so bumping
// updated_at would reset every blocked card's age on the board and bumping
// rev would stale out an open editor save.
func TestSetJobLoadLeavesRevAndUpdatedAt(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "quiet", UpdatedAt: 1700000000000})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	before, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}

	if err := s.SetJobLoad(id, JobLoad{Unblock: 5, Scope: 5, Note: "even", At: time.Now().UnixMilli()}); err != nil {
		t.Fatalf("SetJobLoad: %v", err)
	}
	after, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID after set: %v", err)
	}
	if after.Rev != before.Rev {
		t.Fatalf("rev = %d, want it left at %d — a load write must not stale an open editor save", after.Rev, before.Rev)
	}
	if after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("updated_at = %d, want it left at %d — it is the card's age, not the last agent keystroke", after.UpdatedAt, before.UpdatedAt)
	}
	if after.Load.Unblock != 5 || after.Load.Scope != 5 {
		t.Fatalf("load did not land: %+v", after.Load)
	}
}

// TestSetJobLoadUnknownJobErrors: a zero-row update must say so rather than
// no-op. The caller is a CLI verb whose entire output is a confirmation line,
// so a silent success on a mistyped slug would report a write that never
// happened.
func TestSetJobLoadUnknownJobErrors(t *testing.T) {
	s := openTest(t)
	err := s.SetJobLoad(4242, JobLoad{Unblock: 1, Scope: 1, At: 1})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestSetJobLoadWritesArchivedRows: an archived job is immutable through
// UpdateJobFields (its `archived_at IS NULL` clause), and this write
// deliberately carries no such clause — the row is where the estimate lives
// for the job's whole lifetime, so a job archived mid-estimate keeps a
// writable one rather than failing a write with nowhere else to go.
func TestSetJobLoadWritesArchivedRows(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "shelved"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := s.ArchiveJob(id); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	if err := s.SetJobLoad(id, JobLoad{Unblock: 3, Scope: 5, At: 7}); err != nil {
		t.Fatalf("SetJobLoad on an archived row: %v", err)
	}
	got, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if got.Load.Unblock != 3 || got.Load.Scope != 5 || got.Load.At != 7 {
		t.Fatalf("load = %+v", got.Load)
	}
}

// TestCreateJobDefaultsLoadToNeverEstimated pins the INSERT side: CreateJob
// and CreateArchivedJob both omit the four columns, which is correct only
// because the DEFAULTs spell exactly the "never estimated" state.
func TestCreateJobDefaultsLoadToNeverEstimated(t *testing.T) {
	s := openTest(t)
	if _, err := s.CreateJob(&Job{Slug: "fresh"}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	got, err := s.GetJobBySlug("fresh")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if (got.Load != JobLoad{}) {
		t.Fatalf("CreateJob load = %+v, want the zero JobLoad", got.Load)
	}

	if _, err := s.CreateArchivedJob(&Job{Slug: "old", ArchivedName: "old-1", State: "done"}); err != nil {
		t.Fatalf("CreateArchivedJob: %v", err)
	}
	archived, err := s.ListArchivedJobs()
	if err != nil || len(archived) != 1 {
		t.Fatalf("ListArchivedJobs = %+v, err %v", archived, err)
	}
	if (archived[0].Load != JobLoad{}) {
		t.Fatalf("CreateArchivedJob load = %+v, want the zero JobLoad", archived[0].Load)
	}
}
