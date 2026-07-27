package store

import (
	"reflect"
	"testing"
)

// board_order_test.go — the same four contracts this table always had
// (atomic replace, dedupe + one-column-per-job, skip the non-key, empty
// clears), now expressed in job ids, plus the collision at the bottom that
// only the id can express at all.

func TestSetBoardColumnReplacesAtomically(t *testing.T) {
	s := openTest(t)
	if err := s.SetBoardColumn("queued", []int64{1, 2, 3}); err != nil {
		t.Fatalf("SetBoardColumn 1: %v", err)
	}
	if err := s.SetBoardColumn("working", []int64{9}); err != nil {
		t.Fatalf("SetBoardColumn 2: %v", err)
	}

	order, err := s.GetBoardOrder()
	if err != nil {
		t.Fatalf("GetBoardOrder: %v", err)
	}
	if !reflect.DeepEqual(order["queued"], []int64{1, 2, 3}) {
		t.Fatalf("queued = %+v", order["queued"])
	}
	if !reflect.DeepEqual(order["working"], []int64{9}) {
		t.Fatalf("working = %+v", order["working"])
	}

	// replace queued with a shorter, reordered list — must fully replace,
	// not merge
	if err := s.SetBoardColumn("queued", []int64{3, 1}); err != nil {
		t.Fatalf("SetBoardColumn replace: %v", err)
	}
	order, err = s.GetBoardOrder()
	if err != nil {
		t.Fatalf("GetBoardOrder after replace: %v", err)
	}
	if !reflect.DeepEqual(order["queued"], []int64{3, 1}) {
		t.Fatalf("queued after replace = %+v", order["queued"])
	}
	// untouched column survives
	if !reflect.DeepEqual(order["working"], []int64{9}) {
		t.Fatalf("working after unrelated replace = %+v", order["working"])
	}
}

func TestSetBoardColumnDedupesAndRemovesFromOtherColumns(t *testing.T) {
	s := openTest(t)
	if err := s.SetBoardColumn("working", []int64{1, 2}); err != nil {
		t.Fatalf("seed working: %v", err)
	}
	// job 1 moves to queued, and appears twice in the input — first
	// occurrence wins, and it must vanish from working in the same tx.
	if err := s.SetBoardColumn("queued", []int64{1, 3, 1}); err != nil {
		t.Fatalf("SetBoardColumn queued: %v", err)
	}
	order, err := s.GetBoardOrder()
	if err != nil {
		t.Fatalf("GetBoardOrder: %v", err)
	}
	if !reflect.DeepEqual(order["queued"], []int64{1, 3}) {
		t.Fatalf("queued = %+v, want [1 3]", order["queued"])
	}
	if !reflect.DeepEqual(order["working"], []int64{2}) {
		t.Fatalf("working = %+v, want [2] (1 should have moved out)", order["working"])
	}
}

// TestSetBoardColumnSkipsZeroIDs covers L9's successor: a zero id in the
// input must be skipped entirely, exactly as an empty slug was before v11.
// Zero is not a job — it is what an unpersisted, file-backed *Job carries.
func TestSetBoardColumnSkipsZeroIDs(t *testing.T) {
	s := openTest(t)
	if err := s.SetBoardColumn("queued", []int64{1, 0, 2, 0, 0}); err != nil {
		t.Fatalf("SetBoardColumn: %v", err)
	}
	order, err := s.GetBoardOrder()
	if err != nil {
		t.Fatalf("GetBoardOrder: %v", err)
	}
	if !reflect.DeepEqual(order["queued"], []int64{1, 2}) {
		t.Fatalf("queued = %+v, want [1 2] (zero ids skipped)", order["queued"])
	}
}

func TestSetBoardColumnEmptyClears(t *testing.T) {
	s := openTest(t)
	if err := s.SetBoardColumn("done", []int64{1, 2}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.SetBoardColumn("done", nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	order, err := s.GetBoardOrder()
	if err != nil {
		t.Fatalf("GetBoardOrder: %v", err)
	}
	if len(order["done"]) != 0 {
		t.Fatalf("done not cleared: %+v", order["done"])
	}
}

// TestSetBoardColumnKeepsSameSlugJobsApart is the defect the v11 re-key
// exists for, and it can only be written now: two jobs share the slug
// `helper-catalog` (one archived, one active — archiving FREES the slug), and
// each holds a saved position in a different column. Keyed on the slug, the
// cross-column dedupe `DELETE ... WHERE "column" != ? AND slug = ?` matched
// BOTH, so dragging one card silently evicted the other's position.
func TestSetBoardColumnKeepsSameSlugJobsApart(t *testing.T) {
	s := openTest(t)
	archivedID, err := s.CreateJob(&Job{Slug: "helper-catalog"})
	if err != nil {
		t.Fatalf("CreateJob (to archive): %v", err)
	}
	if _, err := s.ArchiveJob(archivedID); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	activeID, err := s.CreateJob(&Job{Slug: "helper-catalog"})
	if err != nil {
		t.Fatalf("CreateJob (active, reusing the freed slug): %v", err)
	}
	if archivedID == activeID {
		t.Fatal("fixture is wrong: the two generations must be distinct rows")
	}

	if err := s.SetBoardColumn("done", []int64{archivedID}); err != nil {
		t.Fatalf("seed done: %v", err)
	}
	if err := s.SetBoardColumn("queued", []int64{activeID}); err != nil {
		t.Fatalf("place active: %v", err)
	}

	order, err := s.GetBoardOrder()
	if err != nil {
		t.Fatalf("GetBoardOrder: %v", err)
	}
	if !reflect.DeepEqual(order["done"], []int64{archivedID}) {
		t.Fatalf("done = %+v, want the archived job still in place — placing the same-slug ACTIVE job evicted it", order["done"])
	}
	if !reflect.DeepEqual(order["queued"], []int64{activeID}) {
		t.Fatalf("queued = %+v, want [%d]", order["queued"], activeID)
	}
}
