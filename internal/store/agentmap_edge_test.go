package store

import "testing"

// agentmap_edge_test.go — athena-edge's blind suite for the job_messages table
// (50-contract.md G4): a pair bumped in both directions, a stamp that arrives
// out of order, list tie-breaks, and which job lifecycle moves keep or drop the
// rows.

func agentMapEdgeSeed(t *testing.T) (*Store, int64, int64, int64) {
	t.Helper()
	s := openTest(t)
	ids := make([]int64, 3)
	for i, slug := range []string{"alpha", "beta", "gamma"} {
		id, err := s.CreateJob(&Job{Slug: slug, Repo: "kraken"})
		if err != nil {
			t.Fatalf("create %s: %v", slug, err)
		}
		ids[i] = id
	}
	return s, ids[0], ids[1], ids[2]
}

func agentMapEdgeRows(t *testing.T, s *Store) []JobMessage {
	t.Helper()
	rows, err := s.ListJobMessages()
	if err != nil {
		t.Fatalf("ListJobMessages: %v", err)
	}
	return rows
}

func agentMapEdgeFind(rows []JobMessage, from, to int64) *JobMessage {
	for i := range rows {
		if rows[i].FromID == from && rows[i].ToID == to {
			return &rows[i]
		}
	}
	return nil
}

// An empty table lists no rows and no error.
func TestAgentMapEdgeListJobMessagesEmpty(t *testing.T) {
	s, _, _, _ := agentMapEdgeSeed(t)
	if rows := agentMapEdgeRows(t, s); len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
}

// (a, b) and (b, a) are separate rows; bumping one never moves the other.
func TestAgentMapEdgeBumpBothDirectionsAreSeparate(t *testing.T) {
	s, a, b, _ := agentMapEdgeSeed(t)
	for _, step := range []struct{ from, to, at int64 }{
		{a, b, 100}, {a, b, 200}, {b, a, 300}, {a, b, 400},
	} {
		if err := s.BumpJobMessage(step.from, step.to, step.at); err != nil {
			t.Fatalf("bump %d->%d: %v", step.from, step.to, err)
		}
	}
	rows := agentMapEdgeRows(t, s)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want two (one per direction)", rows)
	}
	ab, ba := agentMapEdgeFind(rows, a, b), agentMapEdgeFind(rows, b, a)
	if ab == nil || ab.N != 3 || ab.FirstAt != 100 || ab.LastAt != 400 {
		t.Errorf("a->b = %+v, want N 3, FirstAt 100, LastAt 400", ab)
	}
	if ba == nil || ba.N != 1 || ba.FirstAt != 300 || ba.LastAt != 300 {
		t.Errorf("b->a = %+v, want N 1, both stamps 300", ba)
	}
}

// A later call sets LastAt to its own at, even when that at is earlier than
// the stored one, and never touches FirstAt.
func TestAgentMapEdgeBumpOutOfOrderStamp(t *testing.T) {
	s, a, b, _ := agentMapEdgeSeed(t)
	if err := s.BumpJobMessage(a, b, 500); err != nil {
		t.Fatal(err)
	}
	if err := s.BumpJobMessage(a, b, 50); err != nil {
		t.Fatal(err)
	}
	r := agentMapEdgeFind(agentMapEdgeRows(t, s), a, b)
	if r == nil || r.N != 2 || r.FirstAt != 500 || r.LastAt != 50 {
		t.Fatalf("a->b = %+v, want N 2, FirstAt 500 kept, LastAt 50", r)
	}
}

// Rows with equal LastAt order by FromID.
func TestAgentMapEdgeListTieBreaksOnFromID(t *testing.T) {
	s, a, b, c := agentMapEdgeSeed(t)
	// insert in reverse id order so insertion order cannot fake the tie-break
	if err := s.BumpJobMessage(c, a, 700); err != nil {
		t.Fatal(err)
	}
	if err := s.BumpJobMessage(b, a, 700); err != nil {
		t.Fatal(err)
	}
	if err := s.BumpJobMessage(a, b, 700); err != nil {
		t.Fatal(err)
	}
	if err := s.BumpJobMessage(c, b, 100); err != nil {
		t.Fatal(err)
	}
	rows := agentMapEdgeRows(t, s)
	if len(rows) != 4 {
		t.Fatalf("rows = %+v, want 4", rows)
	}
	if rows[0].FromID != c || rows[0].LastAt != 100 {
		t.Fatalf("rows[0] = %+v, want the LastAt-100 row first", rows[0])
	}
	if rows[1].FromID != a || rows[2].FromID != b || rows[3].FromID != c {
		t.Fatalf("tied rows = %+v, want FromID order %d, %d, %d", rows[1:], a, b, c)
	}
}

// Neither job's rev nor updated_at moves, in either direction.
func TestAgentMapEdgeBumpLeavesBothJobsAlone(t *testing.T) {
	s, a, b, _ := agentMapEdgeSeed(t)
	snap := func(id int64) (int64, int64) {
		j, err := s.GetJobByID(id)
		if err != nil {
			t.Fatalf("get %d: %v", id, err)
		}
		return j.Rev, j.UpdatedAt
	}
	aRev, aUpd := snap(a)
	bRev, bUpd := snap(b)
	for i := int64(0); i < 3; i++ {
		if err := s.BumpJobMessage(a, b, 1000+i); err != nil {
			t.Fatal(err)
		}
		if err := s.BumpJobMessage(b, a, 2000+i); err != nil {
			t.Fatal(err)
		}
	}
	if r, u := snap(a); r != aRev || u != aUpd {
		t.Errorf("a rev/updated_at moved: %d/%d -> %d/%d", aRev, aUpd, r, u)
	}
	if r, u := snap(b); r != bRev || u != bUpd {
		t.Errorf("b rev/updated_at moved: %d/%d -> %d/%d", bRev, bUpd, r, u)
	}
}

// Archiving and unarchiving either side keeps every row; deleting a job
// forever removes the rows naming it on EITHER side and leaves the rest.
func TestAgentMapEdgeArchiveKeepsDeleteDrops(t *testing.T) {
	s, a, b, c := agentMapEdgeSeed(t)
	for _, p := range [][2]int64{{a, b}, {b, a}, {c, b}, {a, c}} {
		if err := s.BumpJobMessage(p[0], p[1], 100); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ArchiveJob(b); err != nil {
		t.Fatalf("archive b: %v", err)
	}
	if rows := agentMapEdgeRows(t, s); len(rows) != 4 {
		t.Fatalf("after archive rows = %+v, want all 4 kept", rows)
	}
	if err := s.UnarchiveJob(b); err != nil {
		t.Fatalf("unarchive b: %v", err)
	}
	if rows := agentMapEdgeRows(t, s); len(rows) != 4 {
		t.Fatalf("after unarchive rows = %+v, want all 4 kept", rows)
	}
	if _, err := s.ArchiveJob(b); err != nil {
		t.Fatalf("re-archive b: %v", err)
	}
	if err := s.DeleteJob(b); err != nil {
		t.Fatalf("delete b: %v", err)
	}
	rows := agentMapEdgeRows(t, s)
	if len(rows) != 1 || rows[0].FromID != a || rows[0].ToID != c {
		t.Fatalf("after delete rows = %+v, want only a->c", rows)
	}
	// the surviving pair keeps counting after another job's delete
	if err := s.BumpJobMessage(a, c, 900); err != nil {
		t.Fatal(err)
	}
	r := agentMapEdgeFind(agentMapEdgeRows(t, s), a, c)
	if r == nil || r.N != 2 || r.FirstAt != 100 || r.LastAt != 900 {
		t.Fatalf("a->c = %+v, want N 2, FirstAt 100, LastAt 900", r)
	}
}
