package store

import (
	"reflect"
	"testing"
)

func TestSetBoardColumnReplacesAtomically(t *testing.T) {
	s := openTest(t)
	if err := s.SetBoardColumn("queued", []string{"a", "b", "c"}); err != nil {
		t.Fatalf("SetBoardColumn 1: %v", err)
	}
	if err := s.SetBoardColumn("working", []string{"x"}); err != nil {
		t.Fatalf("SetBoardColumn 2: %v", err)
	}

	order, err := s.GetBoardOrder()
	if err != nil {
		t.Fatalf("GetBoardOrder: %v", err)
	}
	if !reflect.DeepEqual(order["queued"], []string{"a", "b", "c"}) {
		t.Fatalf("queued = %+v", order["queued"])
	}
	if !reflect.DeepEqual(order["working"], []string{"x"}) {
		t.Fatalf("working = %+v", order["working"])
	}

	// replace queued with a shorter, reordered list — must fully replace,
	// not merge
	if err := s.SetBoardColumn("queued", []string{"c", "a"}); err != nil {
		t.Fatalf("SetBoardColumn replace: %v", err)
	}
	order, err = s.GetBoardOrder()
	if err != nil {
		t.Fatalf("GetBoardOrder after replace: %v", err)
	}
	if !reflect.DeepEqual(order["queued"], []string{"c", "a"}) {
		t.Fatalf("queued after replace = %+v", order["queued"])
	}
	// untouched column survives
	if !reflect.DeepEqual(order["working"], []string{"x"}) {
		t.Fatalf("working after unrelated replace = %+v", order["working"])
	}
}

func TestSetBoardColumnDedupesAndRemovesFromOtherColumns(t *testing.T) {
	s := openTest(t)
	if err := s.SetBoardColumn("working", []string{"a", "b"}); err != nil {
		t.Fatalf("seed working: %v", err)
	}
	// "a" moves to queued, and appears twice in the input — first
	// occurrence wins, and it must vanish from working in the same tx.
	if err := s.SetBoardColumn("queued", []string{"a", "c", "a"}); err != nil {
		t.Fatalf("SetBoardColumn queued: %v", err)
	}
	order, err := s.GetBoardOrder()
	if err != nil {
		t.Fatalf("GetBoardOrder: %v", err)
	}
	if !reflect.DeepEqual(order["queued"], []string{"a", "c"}) {
		t.Fatalf("queued = %+v, want [a c]", order["queued"])
	}
	if !reflect.DeepEqual(order["working"], []string{"b"}) {
		t.Fatalf("working = %+v, want [b] (a should have moved out)", order["working"])
	}
}

// TestSetBoardColumnSkipsEmptySlugs covers L9: an empty slug in the input
// must be skipped entirely, matching the file path's SaveColumnOrder.
func TestSetBoardColumnSkipsEmptySlugs(t *testing.T) {
	s := openTest(t)
	if err := s.SetBoardColumn("queued", []string{"a", "", "b", "", ""}); err != nil {
		t.Fatalf("SetBoardColumn: %v", err)
	}
	order, err := s.GetBoardOrder()
	if err != nil {
		t.Fatalf("GetBoardOrder: %v", err)
	}
	if !reflect.DeepEqual(order["queued"], []string{"a", "b"}) {
		t.Fatalf("queued = %+v, want [a b] (empty slugs skipped)", order["queued"])
	}
}

func TestSetBoardColumnEmptyClears(t *testing.T) {
	s := openTest(t)
	if err := s.SetBoardColumn("done", []string{"a", "b"}); err != nil {
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
