package jobs

import (
	"fmt"
	"testing"
)

// mappos_regress_test.go — regression pins for review round one of the agent
// map (contract G7, amendment 6): a full row evicts the keys written longest
// ago instead of refusing every save that adds a card, a coordinate past
// ±10,000,000 is refused, and a row saved in the older `[x, y]` shape loads
// and counts as oldest.

func TestMapPosRegressEvictsOldestAtTheCap(t *testing.T) {
	useTestStore(t)
	first := map[string][2]float64{}
	for i := 0; i < mapPositionsMax; i++ {
		first[fmt.Sprintf("old-%04d", i)] = [2]float64{float64(i), 0}
	}
	if err := SetMapPositions(first); err != nil {
		t.Fatalf("fill: %v", err)
	}
	// rewrite one old key so it is now the newest of the first batch
	if err := SetMapPositions(map[string][2]float64{"old-0000": {1, 1}}); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := SetMapPositions(map[string][2]float64{"new-a": {5, 5}, "new-b": {6, 6}}); err != nil {
		t.Fatalf("a save over the cap must evict, not refuse: %v", err)
	}
	got := MapPositions()
	if len(got) != mapPositionsMax {
		t.Fatalf("len = %d, want %d", len(got), mapPositionsMax)
	}
	for _, k := range []string{"new-a", "new-b", "old-0000"} {
		if _, ok := got[k]; !ok {
			t.Errorf("%s was evicted, want kept", k)
		}
	}
	// the batch's two oldest by key among equal stamps: old-0001, old-0002
	for _, k := range []string{"old-0001", "old-0002"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s kept, want evicted as the oldest-written", k)
		}
	}
	if _, ok := got["old-0003"]; !ok {
		t.Errorf("old-0003 evicted, want kept")
	}
}

func TestMapPosRegressOneSaveOverTheCapIsRefused(t *testing.T) {
	useTestStore(t)
	big := map[string][2]float64{}
	for i := 0; i <= mapPositionsMax; i++ {
		big[fmt.Sprintf("k%d", i)] = [2]float64{1, 2}
	}
	if err := SetMapPositions(big); err == nil {
		t.Fatal("a single save of more than the cap was accepted")
	}
	if got := MapPositions(); len(got) != 0 {
		t.Fatalf("a refused save stored %d keys", len(got))
	}
}

func TestMapPosRegressCoordinateBound(t *testing.T) {
	useTestStore(t)
	for _, c := range []float64{10_000_000.5, -10_000_001, 1e300} {
		if err := SetMapPositions(map[string][2]float64{"a": {c, 0}}); err == nil {
			t.Errorf("x = %v accepted", c)
		}
		if err := SetMapPositions(map[string][2]float64{"a": {0, c}}); err == nil {
			t.Errorf("y = %v accepted", c)
		}
	}
	if err := SetMapPositions(map[string][2]float64{"a": {10_000_000, -10_000_000}}); err != nil {
		t.Fatalf("the bound itself refused: %v", err)
	}
	if got := MapPositions()["a"]; got != [2]float64{10_000_000, -10_000_000} {
		t.Fatalf("a = %v", got)
	}
}

func TestMapPosRegressTwoNumberRowLoadsAsOldest(t *testing.T) {
	st := useTestStore(t)
	if err := st.SetKV(mapPositionsKey, `{"legacy":[3,4]}`); err != nil {
		t.Fatal(err)
	}
	if got := MapPositions()["legacy"]; got != [2]float64{3, 4} {
		t.Fatalf("legacy = %v, want [3 4]", got)
	}
	fill := map[string][2]float64{}
	for i := 0; i < mapPositionsMax; i++ {
		fill[fmt.Sprintf("n%04d", i)] = [2]float64{0, 0}
	}
	if err := SetMapPositions(fill); err != nil {
		t.Fatal(err)
	}
	got := MapPositions()
	if _, ok := got["legacy"]; ok {
		t.Fatal("an unstamped key outlived stamped ones at the cap")
	}
	if len(got) != mapPositionsMax {
		t.Fatalf("len = %d", len(got))
	}
}
