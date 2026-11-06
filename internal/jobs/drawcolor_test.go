package jobs

import "testing"

// drawcolor_test.go — a drawing's ink (contract Amendment 3): Color is
// optional and, when set, one of Palette's eight names; anything else refuses
// the save, and a saved name comes back as given.

func TestDrawColorAcceptedAndKept(t *testing.T) {
	useTestStore(t)
	set := map[string]MapDrawing{}
	for i, c := range Palette {
		set[string(rune('a'+i))] = MapDrawing{Kind: "line", Pts: []float64{0, 0, 1, 1}, Size: 2, Color: c.Name}
	}
	set["plain"] = MapDrawing{Kind: "line", Pts: []float64{0, 0, 1, 1}, Size: 2}
	if err := SetMapDrawings(set, nil); err != nil {
		t.Fatalf("SetMapDrawings: %v", err)
	}
	got := MapDrawings()
	if len(got) != len(set) {
		t.Fatalf("saved %d drawings, want %d", len(got), len(set))
	}
	for id, d := range set {
		if got[id].Color != d.Color {
			t.Errorf("%s: color %q, want %q", id, got[id].Color, d.Color)
		}
	}
}

func TestDrawColorRefused(t *testing.T) {
	useTestStore(t)
	for _, c := range []string{"#ff0000", "Red", "ink", "default", " red"} {
		d := MapDrawing{Kind: "rect", Pts: []float64{0, 0, 1, 1}, Size: 2, Color: c}
		if err := SetMapDrawings(map[string]MapDrawing{"x": d}, nil); err == nil {
			t.Errorf("color %q accepted, want refused", c)
		}
	}
	if got := MapDrawings(); len(got) != 0 {
		t.Errorf("a refused save stored %v", got)
	}
}
