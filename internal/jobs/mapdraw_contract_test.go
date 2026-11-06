package jobs

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"kraken/internal/store"
)

// mapdraw_contract_test.go — athena-logic's blind tests for the server half of
// map drawings (50-contract.md A1): MapDrawings and SetMapDrawings over the
// one kv row, their invariants and their ordinary successful paths. Refusals
// beyond one representative case belong to athena-edge's suite.

func mapDrawContractPen(x float64) MapDrawing {
	return MapDrawing{Kind: "pen", Pts: []float64{x, 0, x + 10, 5, x + 20, 0}, Size: 3}
}

func mapDrawContractRect() MapDrawing {
	return MapDrawing{Kind: "rect", Pts: []float64{50, 60, 10, 20}, Size: 1.5}
}

func mapDrawContractText() MapDrawing {
	return MapDrawing{Kind: "text", Pts: []float64{100, 200}, Size: 18, Text: "hello\nworld"}
}

// mapDrawContractRow reads the stored kv row and decodes it as the contract
// says it is shaped: one JSON object from id to MapDrawing.
func mapDrawContractRow(t *testing.T, st *store.Store) map[string]MapDrawing {
	t.Helper()
	raw, err := st.GetKV(mapDrawingsKey)
	if err != nil {
		t.Fatalf("GetKV(%s): %v", mapDrawingsKey, err)
	}
	var out map[string]MapDrawing
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("row %q is not an object of drawings: %v", raw, err)
	}
	return out
}

func mapDrawContractMustSet(t *testing.T, set map[string]MapDrawing, remove []string) {
	t.Helper()
	if err := SetMapDrawings(set, remove); err != nil {
		t.Fatalf("SetMapDrawings(%d set, %v remove): %v", len(set), remove, err)
	}
}

func TestMapDrawContractConstants(t *testing.T) {
	if mapDrawingsKey != "map_drawings" {
		t.Errorf("mapDrawingsKey = %q, want map_drawings", mapDrawingsKey)
	}
	if mapDrawingsMax != 1000 {
		t.Errorf("mapDrawingsMax = %d, want 1000", mapDrawingsMax)
	}
}

// MapDrawing's JSON is the wire shape: kind, pts, size, and text only when set.
func TestMapDrawContractDrawingJSONShape(t *testing.T) {
	b, err := json.Marshal(MapDrawing{Kind: "line", Pts: []float64{1, 2, 3, 4}, Size: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"kind":"line","pts":[1,2,3,4],"size":2}`; got != want {
		t.Errorf("line JSON = %s, want %s", got, want)
	}
	b, err = json.Marshal(MapDrawing{Kind: "text", Pts: []float64{1, 2}, Size: 18, Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"kind":"text","pts":[1,2],"size":18,"text":"hi"}`; got != want {
		t.Errorf("text JSON = %s, want %s", got, want)
	}
}

func TestMapDrawContractEmptyWhenNoneSaved(t *testing.T) {
	useTestStore(t)
	got := MapDrawings()
	if got == nil {
		t.Fatal("MapDrawings() = nil, want a non-nil empty map")
	}
	if len(got) != 0 {
		t.Fatalf("MapDrawings() = %v, want empty", got)
	}
}

func TestMapDrawContractEmptyWhenNoStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	UseStore(nil)
	got := MapDrawings()
	if got == nil || len(got) != 0 {
		t.Fatalf("MapDrawings() with no store = %#v, want a non-nil empty map", got)
	}
}

func TestMapDrawContractEmptyWhenRowDoesNotParse(t *testing.T) {
	st := useTestStore(t)
	if err := st.SetKV(mapDrawingsKey, "{not json"); err != nil {
		t.Fatal(err)
	}
	got := MapDrawings()
	if got == nil || len(got) != 0 {
		t.Fatalf("MapDrawings() over a corrupt row = %#v, want a non-nil empty map", got)
	}
}

// A stored entry that fails V is left out; the rest are still returned.
func TestMapDrawContractInvalidStoredEntryLeftOut(t *testing.T) {
	st := useTestStore(t)
	row := `{"good":{"kind":"line","pts":[0,0,10,10],"size":2},` +
		`"badkind":{"kind":"star","pts":[0,0,10,10],"size":2},` +
		`"badsize":{"kind":"line","pts":[0,0,10,10],"size":0},` +
		`"also":{"kind":"text","pts":[5,6],"size":18,"text":"ok"}}`
	if err := st.SetKV(mapDrawingsKey, row); err != nil {
		t.Fatal(err)
	}
	got := MapDrawings()
	want := map[string]MapDrawing{
		"good": {Kind: "line", Pts: []float64{0, 0, 10, 10}, Size: 2},
		"also": {Kind: "text", Pts: []float64{5, 6}, Size: 18, Text: "ok"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MapDrawings() = %#v, want %#v", got, want)
	}
}

// A1.1: a set is saved and read back unchanged, every kind included.
func TestMapDrawContractSetRoundTrips(t *testing.T) {
	st := useTestStore(t)
	set := map[string]MapDrawing{
		"p1": mapDrawContractPen(0),
		"l1": {Kind: "line", Pts: []float64{-5, 2.5, 30, 40}, Size: 2},
		"r1": mapDrawContractRect(),
		"e1": {Kind: "ellipse", Pts: []float64{0, 0, 100, 50}, Size: 4},
		"t1": mapDrawContractText(),
	}
	mapDrawContractMustSet(t, set, nil)
	if got := MapDrawings(); !reflect.DeepEqual(got, set) {
		t.Fatalf("MapDrawings() = %#v, want %#v", got, set)
	}
	if row := mapDrawContractRow(t, st); !reflect.DeepEqual(row, set) {
		t.Fatalf("stored row = %#v, want %#v", row, set)
	}
}

// A1.1: ids a call does not name are kept unchanged.
func TestMapDrawContractUnnamedIdsKept(t *testing.T) {
	useTestStore(t)
	mapDrawContractMustSet(t, map[string]MapDrawing{"a": mapDrawContractPen(0), "b": mapDrawContractRect()}, nil)
	mapDrawContractMustSet(t, map[string]MapDrawing{"c": mapDrawContractText()}, nil)
	want := map[string]MapDrawing{"a": mapDrawContractPen(0), "b": mapDrawContractRect(), "c": mapDrawContractText()}
	if got := MapDrawings(); !reflect.DeepEqual(got, want) {
		t.Fatalf("MapDrawings() = %#v, want %#v", got, want)
	}
}

// A1.1: setting a saved id replaces that drawing.
func TestMapDrawContractSetReplaces(t *testing.T) {
	useTestStore(t)
	mapDrawContractMustSet(t, map[string]MapDrawing{"a": mapDrawContractPen(0), "b": mapDrawContractRect()}, nil)
	mapDrawContractMustSet(t, map[string]MapDrawing{"a": mapDrawContractText()}, nil)
	want := map[string]MapDrawing{"a": mapDrawContractText(), "b": mapDrawContractRect()}
	if got := MapDrawings(); !reflect.DeepEqual(got, want) {
		t.Fatalf("MapDrawings() = %#v, want %#v", got, want)
	}
}

// A1.1: remove deletes the named ids and keeps the rest.
func TestMapDrawContractRemoveDeletes(t *testing.T) {
	useTestStore(t)
	mapDrawContractMustSet(t, map[string]MapDrawing{"a": mapDrawContractPen(0), "b": mapDrawContractRect(), "c": mapDrawContractText()}, nil)
	mapDrawContractMustSet(t, nil, []string{"a", "c"})
	want := map[string]MapDrawing{"b": mapDrawContractRect()}
	if got := MapDrawings(); !reflect.DeepEqual(got, want) {
		t.Fatalf("MapDrawings() = %#v, want %#v", got, want)
	}
}

// A1.1: one call may both remove and set (different ids).
func TestMapDrawContractRemoveAndSetInOneCall(t *testing.T) {
	useTestStore(t)
	mapDrawContractMustSet(t, map[string]MapDrawing{"a": mapDrawContractPen(0), "b": mapDrawContractRect()}, nil)
	mapDrawContractMustSet(t, map[string]MapDrawing{"c": mapDrawContractText()}, []string{"a"})
	want := map[string]MapDrawing{"b": mapDrawContractRect(), "c": mapDrawContractText()}
	if got := MapDrawings(); !reflect.DeepEqual(got, want) {
		t.Fatalf("MapDrawings() = %#v, want %#v", got, want)
	}
}

// A1.2: removing an unsaved id is a no-op, and a call made only of such a
// remove succeeds.
func TestMapDrawContractRemoveUnknownIsNoop(t *testing.T) {
	useTestStore(t)
	mapDrawContractMustSet(t, map[string]MapDrawing{"a": mapDrawContractPen(0)}, nil)
	if err := SetMapDrawings(nil, []string{"never-saved"}); err != nil {
		t.Fatalf("remove of an unsaved id: %v, want nil", err)
	}
	want := map[string]MapDrawing{"a": mapDrawContractPen(0)}
	if got := MapDrawings(); !reflect.DeepEqual(got, want) {
		t.Fatalf("MapDrawings() = %#v, want %#v", got, want)
	}
}

func TestMapDrawContractRemoveUnknownOnEmptyRow(t *testing.T) {
	useTestStore(t)
	if err := SetMapDrawings(map[string]MapDrawing{}, []string{"ghost"}); err != nil {
		t.Fatalf("remove of an unsaved id on an empty row: %v, want nil", err)
	}
	if got := MapDrawings(); len(got) != 0 {
		t.Fatalf("MapDrawings() = %#v, want empty", got)
	}
}

// A1.3, one representative refusal: a call with an invalid drawing stores
// and changes nothing, the call's valid ids included.
func TestMapDrawContractRefusalChangesNothing(t *testing.T) {
	useTestStore(t)
	mapDrawContractMustSet(t, map[string]MapDrawing{"a": mapDrawContractPen(0)}, nil)
	err := SetMapDrawings(map[string]MapDrawing{
		"ok":  mapDrawContractRect(),
		"bad": {Kind: "line", Pts: []float64{0, 0, 1}, Size: 2},
	}, []string{"a"})
	if err == nil {
		t.Fatal("SetMapDrawings with an invalid drawing: nil error, want a refusal")
	}
	want := map[string]MapDrawing{"a": mapDrawContractPen(0)}
	if got := MapDrawings(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after a refusal MapDrawings() = %#v, want %#v", got, want)
	}
}

func mapDrawContractFill(t *testing.T, n int) map[string]MapDrawing {
	t.Helper()
	set := make(map[string]MapDrawing, n)
	for i := 0; i < n; i++ {
		set[fmt.Sprintf("d%04d", i)] = mapDrawContractPen(float64(i))
	}
	mapDrawContractMustSet(t, set, nil)
	return set
}

// A1.3/A1.4: a row of exactly 1000 is allowed.
func TestMapDrawContractFullRowAllowed(t *testing.T) {
	useTestStore(t)
	mapDrawContractFill(t, 1000)
	if got := len(MapDrawings()); got != 1000 {
		t.Fatalf("len(MapDrawings()) = %d, want 1000", got)
	}
}

// A1.4: the cap is checked after the removes.
func TestMapDrawContractCapCheckedAfterRemoves(t *testing.T) {
	useTestStore(t)
	mapDrawContractFill(t, 1000)
	if err := SetMapDrawings(map[string]MapDrawing{"fresh": mapDrawContractText()}, []string{"d0000"}); err != nil {
		t.Fatalf("remove one + set one on a full row: %v, want nil", err)
	}
	got := MapDrawings()
	if len(got) != 1000 {
		t.Fatalf("len = %d, want 1000", len(got))
	}
	if _, ok := got["d0000"]; ok {
		t.Error("d0000 still saved after its remove")
	}
	if d, ok := got["fresh"]; !ok || !reflect.DeepEqual(d, mapDrawContractText()) {
		t.Errorf("fresh = %#v (present %v), want the text drawing", d, ok)
	}
}

// A1.4: going over the cap is refused, the error names 1000, nothing changes.
func TestMapDrawContractOverCapRefused(t *testing.T) {
	useTestStore(t)
	before := mapDrawContractFill(t, 1000)
	err := SetMapDrawings(map[string]MapDrawing{"one-more": mapDrawContractText()}, nil)
	if err == nil {
		t.Fatal("1001st drawing: nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "1000") {
		t.Errorf("over-cap error %q does not contain 1000", err.Error())
	}
	if got := MapDrawings(); !reflect.DeepEqual(got, before) {
		t.Fatalf("row changed after an over-cap refusal: %d drawings", len(got))
	}
}

// A1.4: replacing a saved id on a full row does not grow the result.
func TestMapDrawContractReplaceOnFullRow(t *testing.T) {
	useTestStore(t)
	mapDrawContractFill(t, 1000)
	if err := SetMapDrawings(map[string]MapDrawing{"d0500": mapDrawContractText()}, nil); err != nil {
		t.Fatalf("replace on a full row: %v, want nil", err)
	}
	got := MapDrawings()
	if len(got) != 1000 || !reflect.DeepEqual(got["d0500"], mapDrawContractText()) {
		t.Fatalf("len %d, d0500 = %#v", len(got), got["d0500"])
	}
}

// A1.5: a corrupt row is replaced by the call's result.
func TestMapDrawContractCorruptRowReplaced(t *testing.T) {
	st := useTestStore(t)
	if err := st.SetKV(mapDrawingsKey, `["not", "an", "object"`); err != nil {
		t.Fatal(err)
	}
	set := map[string]MapDrawing{"x": mapDrawContractRect()}
	mapDrawContractMustSet(t, set, []string{"y"})
	if got := MapDrawings(); !reflect.DeepEqual(got, set) {
		t.Fatalf("MapDrawings() = %#v, want %#v", got, set)
	}
	if row := mapDrawContractRow(t, st); !reflect.DeepEqual(row, set) {
		t.Fatalf("stored row = %#v, want %#v", row, set)
	}
}

// A1.6: concurrent calls never drop each other's ids.
func TestMapDrawContractConcurrentSetsKeepAll(t *testing.T) {
	useTestStore(t)
	const n = 40
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- SetMapDrawings(map[string]MapDrawing{fmt.Sprintf("g%02d", i): mapDrawContractPen(float64(i))}, nil)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent SetMapDrawings: %v", err)
		}
	}
	got := MapDrawings()
	if len(got) != n {
		t.Fatalf("len(MapDrawings()) = %d, want %d", len(got), n)
	}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("g%02d", i)
		if d, ok := got[id]; !ok || !reflect.DeepEqual(d, mapDrawContractPen(float64(i))) {
			t.Errorf("%s = %#v (present %v)", id, d, ok)
		}
	}
}

// A1.6: concurrent removes and sets on disjoint ids all land.
func TestMapDrawContractConcurrentMixed(t *testing.T) {
	useTestStore(t)
	const n = 20
	seed := map[string]MapDrawing{}
	for i := 0; i < n; i++ {
		seed[fmt.Sprintf("old%02d", i)] = mapDrawContractRect()
	}
	mapDrawContractMustSet(t, seed, nil)
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- SetMapDrawings(
				map[string]MapDrawing{fmt.Sprintf("new%02d", i): mapDrawContractText()},
				[]string{fmt.Sprintf("old%02d", i)},
			)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent SetMapDrawings: %v", err)
		}
	}
	got := MapDrawings()
	if len(got) != n {
		t.Fatalf("len = %d, want %d: %v", len(got), n, got)
	}
	for i := 0; i < n; i++ {
		if _, ok := got[fmt.Sprintf("new%02d", i)]; !ok {
			t.Errorf("new%02d missing", i)
		}
		if _, ok := got[fmt.Sprintf("old%02d", i)]; ok {
			t.Errorf("old%02d still present", i)
		}
	}
}
