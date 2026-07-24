package store

import (
	"fmt"
	"testing"
)

// TestAppendEventPrunesOldRows shrinks maxEvents/pruneCheckEvery for the
// test rather than inserting 50,000+ rows: the prune logic itself doesn't
// care about the actual thresholds, only that it fires periodically and
// keeps the newest rows.
func TestAppendEventPrunesOldRows(t *testing.T) {
	st := openTest(t)

	origMax, origCheck := maxEvents, pruneCheckEvery
	maxEvents, pruneCheckEvery = 10, 5
	t.Cleanup(func() { maxEvents, pruneCheckEvery = origMax, origCheck })

	var lastID int64
	const total = 23
	for i := 0; i < total; i++ {
		id, err := st.AppendEvent(Event{Src: "test", Event: "e", Msg: fmt.Sprintf("m%d", i)})
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		lastID = id
	}

	rows, err := st.ListEvents(1000, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(rows) >= total {
		t.Fatalf("want pruning to have reduced the row count below %d, got %d", total, len(rows))
	}
	if maxWant := maxEvents + int(pruneCheckEvery) - 1; len(rows) > maxWant {
		t.Fatalf("row count %d exceeds the max prune lag (%d)", len(rows), maxWant)
	}

	found := false
	for _, r := range rows {
		if r.ID == lastID {
			found = true
		}
	}
	if !found {
		t.Fatal("newest event was pruned — pruning must never drop recent rows")
	}
}

// TestAppendEventPruneKeepsIDMonotonic covers the pruneCheckEvery gate
// itself: with the default (unshrunk) settings, a handful of inserts must
// never trigger a prune at all, and every one of them must still be
// present afterward.
func TestAppendEventFewInsertsNeverPruned(t *testing.T) {
	st := openTest(t)

	for i := 0; i < 5; i++ {
		if _, err := st.AppendEvent(Event{Src: "test", Event: "e"}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	rows, err := st.ListEvents(1000, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(rows) != 5 {
		t.Fatalf("want all 5 events present (no pruning yet), got %d", len(rows))
	}
}
