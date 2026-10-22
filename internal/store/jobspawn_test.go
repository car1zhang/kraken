package store

import (
	"path/filepath"
	"testing"
)

// jobspawn_test.go — the job_spawns table (jobspawn.go, schema v38):
// first writer wins, the whole table lists in stamp order, a hard delete takes
// the rows on both sides, and neither job's rev or updated_at moves.

func seedSpawnJobs(t *testing.T) (*Store, int64, int64, int64) {
	t.Helper()
	s := openTest(t)
	parent, err := s.CreateJob(&Job{Slug: "parent", Repo: "kraken"})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	child, err := s.CreateJob(&Job{Slug: "child", Repo: "kraken"})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	other, err := s.CreateJob(&Job{Slug: "other", Repo: "kraken"})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	return s, parent, child, other
}

func TestSetJobSpawnFirstWriterWins(t *testing.T) {
	s, parent, child, other := seedSpawnJobs(t)
	ins, err := s.SetJobSpawn(child, parent, 100, "draft")
	if err != nil || !ins {
		t.Fatalf("first set: inserted=%v err=%v", ins, err)
	}
	ins, err = s.SetJobSpawn(child, other, 200, "submit")
	if err != nil {
		t.Fatalf("second set: %v", err)
	}
	if ins {
		t.Fatal("second set reported an insert over an existing row")
	}
	rows, err := s.ListJobSpawns()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].ParentID != parent || rows[0].Via != "draft" || rows[0].At != 100 {
		t.Fatalf("rows = %+v, want the first stamp kept", rows)
	}
}

func TestListJobSpawnsOrdersByStamp(t *testing.T) {
	s, parent, child, other := seedSpawnJobs(t)
	if _, err := s.SetJobSpawn(other, parent, 300, "submit"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetJobSpawn(child, parent, 100, "draft"); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListJobSpawns()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 || rows[0].ChildID != child || rows[1].ChildID != other {
		t.Fatalf("rows = %+v, want child (at 100) before other (at 300)", rows)
	}
}

func TestSetJobSpawnLeavesRevAndUpdatedAtAlone(t *testing.T) {
	s, parent, child, _ := seedSpawnJobs(t)
	before := func(id int64) (int64, int64) {
		j, err := s.GetJobByID(id)
		if err != nil {
			t.Fatalf("get %d: %v", id, err)
		}
		return j.Rev, j.UpdatedAt
	}
	pRev, pUpd := before(parent)
	cRev, cUpd := before(child)
	if _, err := s.SetJobSpawn(child, parent, 100, "draft"); err != nil {
		t.Fatal(err)
	}
	if r, u := before(parent); r != pRev || u != pUpd {
		t.Fatalf("parent rev/updated_at moved: %d/%d -> %d/%d", pRev, pUpd, r, u)
	}
	if r, u := before(child); r != cRev || u != cUpd {
		t.Fatalf("child rev/updated_at moved: %d/%d -> %d/%d", cRev, cUpd, r, u)
	}
}

func TestDeleteJobRemovesSpawnsOnBothSides(t *testing.T) {
	s, parent, child, other := seedSpawnJobs(t)
	if _, err := s.SetJobSpawn(child, parent, 100, "draft"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetJobSpawn(other, child, 200, "submit"); err != nil {
		t.Fatal(err)
	}
	// DeleteJob only deletes archived rows in practice, but the store verb
	// itself is a bare delete by id; archive first to mirror the real path.
	if _, err := s.ArchiveJob(child); err != nil {
		t.Fatalf("archive child: %v", err)
	}
	if err := s.DeleteJob(child); err != nil {
		t.Fatalf("delete child: %v", err)
	}
	rows, err := s.ListJobSpawns()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows after deleting the child = %+v, want none (it was a child of one row and the parent of another)", rows)
	}
}

func TestMigrateV37ToV38AddsJobSpawns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	// Roll back to v37 by dropping the table and resetting user_version, then
	// re-open: migrate() must walk 37 -> 38 and recreate it.
	if _, err := s.db.Exec(`DROP TABLE job_spawns`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 37`); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	s.Close()
	s, err = Open(dbPath)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer s.Close()
	if _, err := s.ListJobSpawns(); err != nil {
		t.Fatalf("job_spawns missing after migrating from v37: %v", err)
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}
}
