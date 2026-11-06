package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	"kraken/internal/store"
)

// mapdraw_edge_test.go: athena-edge's blind suite for the saved map drawings
// (50-contract.md A1 and the shared rule V): every refusal SetMapDrawings
// promises and that it stores nothing when it refuses, each cap at and just
// past its limit, the result-side cap, removes of unsaved ids, a corrupt row,
// stored entries that fail V, and two saves racing for the last slot.

// mapDrawEdgeLine is a valid line drawing.
func mapDrawEdgeLine() MapDrawing {
	return MapDrawing{Kind: "line", Pts: []float64{0, 0, 10, 10}, Size: 2}
}

// mapDrawEdgeOf returns a valid drawing of the given kind.
func mapDrawEdgeOf(kind string) MapDrawing {
	switch kind {
	case "pen":
		return MapDrawing{Kind: "pen", Pts: []float64{0, 0, 5, 5, 10, 0}, Size: 3}
	case "text":
		return MapDrawing{Kind: "text", Pts: []float64{1, 2}, Size: 18, Text: "hello"}
	default:
		return MapDrawing{Kind: kind, Pts: []float64{0, 0, 10, 10}, Size: 2}
	}
}

// mapDrawEdgeRaw returns the kv row's raw value and whether it exists.
func mapDrawEdgeRaw(t *testing.T, st *store.Store) (string, bool) {
	t.Helper()
	raw, err := st.GetKV(mapDrawingsKey)
	if errors.Is(err, store.ErrNotFound) {
		return "", false
	}
	if err != nil {
		t.Fatalf("GetKV: %v", err)
	}
	return raw, true
}

// mapDrawEdgeSeed saves one drawing under "seed" and returns the row as stored.
func mapDrawEdgeSeed(t *testing.T, st *store.Store) string {
	t.Helper()
	if err := SetMapDrawings(map[string]MapDrawing{"seed": mapDrawEdgeLine()}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	raw, ok := mapDrawEdgeRaw(t, st)
	if !ok {
		t.Fatal("seed: no row written")
	}
	return raw
}

// mapDrawEdgeFill writes n valid drawings straight into the row, ids d0000…
func mapDrawEdgeFill(t *testing.T, st *store.Store, n int) {
	t.Helper()
	row := make(map[string]MapDrawing, n)
	for i := 0; i < n; i++ {
		row[mapDrawEdgeID(i)] = mapDrawEdgeLine()
	}
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV(mapDrawingsKey, string(data)); err != nil {
		t.Fatalf("SetKV: %v", err)
	}
}

func mapDrawEdgeID(i int) string { return fmt.Sprintf("d%04d", i) }

// mapDrawEdgeRefused asserts the call errs and leaves the row byte-identical.
func mapDrawEdgeRefused(t *testing.T, st *store.Store, name string, set map[string]MapDrawing, remove []string) {
	t.Helper()
	before, had := mapDrawEdgeRaw(t, st)
	if err := SetMapDrawings(set, remove); err == nil {
		t.Errorf("%s: SetMapDrawings returned nil, want an error", name)
	}
	after, has := mapDrawEdgeRaw(t, st)
	if had != has || before != after {
		t.Errorf("%s: row changed on a refusal: before %q (exists %v), after %q (exists %v)", name, before, had, after, has)
	}
}

// --- no store ---------------------------------------------------------------

func TestMapDrawEdgeNoStoreReadIsEmptyNonNil(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	UseStore(nil)
	got := MapDrawings()
	if got == nil {
		t.Fatal("MapDrawings() with no store = nil, want a non-nil empty map")
	}
	if len(got) != 0 {
		t.Fatalf("MapDrawings() with no store = %v, want empty", got)
	}
}

func TestMapDrawEdgeNoStoreSetRefused(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	UseStore(nil)
	if err := SetMapDrawings(map[string]MapDrawing{"a": mapDrawEdgeLine()}, nil); err == nil {
		t.Fatal("SetMapDrawings with no store returned nil, want an error")
	}
	if err := SetMapDrawings(nil, []string{"a"}); err == nil {
		t.Fatal("remove-only SetMapDrawings with no store returned nil, want an error")
	}
}

// --- empty calls ------------------------------------------------------------

func TestMapDrawEdgeEmptyCallRefused(t *testing.T) {
	st := useTestStore(t)
	mapDrawEdgeRefused(t, st, "nil, nil on no row", nil, nil)
	mapDrawEdgeSeed(t, st)
	mapDrawEdgeRefused(t, st, "nil, nil", nil, nil)
	mapDrawEdgeRefused(t, st, "empty map, empty slice", map[string]MapDrawing{}, []string{})
}

func TestMapDrawEdgeReadWithNoRowIsEmptyNonNil(t *testing.T) {
	useTestStore(t)
	got := MapDrawings()
	if got == nil || len(got) != 0 {
		t.Fatalf("MapDrawings() on a fresh store = %#v, want a non-nil empty map", got)
	}
}

// --- ids ------------------------------------------------------------------------

func TestMapDrawEdgeIDAtSixtyFourBytesAccepted(t *testing.T) {
	useTestStore(t)
	id := strings.Repeat("a", 64)
	if err := SetMapDrawings(map[string]MapDrawing{id: mapDrawEdgeLine()}, nil); err != nil {
		t.Fatalf("64-byte id refused: %v", err)
	}
	if _, ok := MapDrawings()[id]; !ok {
		t.Fatal("64-byte id not saved")
	}
}

func TestMapDrawEdgeMultibyteIDAtSixtyFourBytesAccepted(t *testing.T) {
	useTestStore(t)
	id := strings.Repeat("é", 32) // 64 bytes, 32 runes
	if err := SetMapDrawings(map[string]MapDrawing{id: mapDrawEdgeLine()}, nil); err != nil {
		t.Fatalf("64-byte multibyte id refused: %v", err)
	}
	if _, ok := MapDrawings()[id]; !ok {
		t.Fatal("64-byte multibyte id not saved")
	}
}

func TestMapDrawEdgeBadIDsRefused(t *testing.T) {
	st := useTestStore(t)
	mapDrawEdgeSeed(t, st)
	long := strings.Repeat("a", 65)
	longRunes := strings.Repeat("é", 33) // 33 runes, 66 bytes: over by bytes
	mapDrawEdgeRefused(t, st, "empty id in set", map[string]MapDrawing{"": mapDrawEdgeLine()}, nil)
	mapDrawEdgeRefused(t, st, "65-byte id in set", map[string]MapDrawing{long: mapDrawEdgeLine()}, nil)
	mapDrawEdgeRefused(t, st, "66-byte multibyte id in set", map[string]MapDrawing{longRunes: mapDrawEdgeLine()}, nil)
	mapDrawEdgeRefused(t, st, "empty id in remove", nil, []string{""})
	mapDrawEdgeRefused(t, st, "65-byte id in remove", nil, []string{long})
	mapDrawEdgeRefused(t, st, "a bad remove id beside a good set", map[string]MapDrawing{"ok": mapDrawEdgeLine()}, []string{""})
	mapDrawEdgeRefused(t, st, "a bad set id beside a good one", map[string]MapDrawing{"ok": mapDrawEdgeLine(), long: mapDrawEdgeLine()}, nil)
}

func TestMapDrawEdgeIDInSetAndRemoveRefused(t *testing.T) {
	st := useTestStore(t)
	mapDrawEdgeSeed(t, st)
	mapDrawEdgeRefused(t, st, "new id in both", map[string]MapDrawing{"x": mapDrawEdgeLine()}, []string{"x"})
	mapDrawEdgeRefused(t, st, "saved id in both", map[string]MapDrawing{"seed": mapDrawEdgeOf("rect")}, []string{"seed"})
}

// --- V --------------------------------------------------------------------------

func TestMapDrawEdgeEveryKindAccepted(t *testing.T) {
	useTestStore(t)
	set := map[string]MapDrawing{}
	for _, k := range []string{"pen", "line", "rect", "ellipse", "text"} {
		set[k] = mapDrawEdgeOf(k)
	}
	if err := SetMapDrawings(set, nil); err != nil {
		t.Fatalf("one of each kind refused: %v", err)
	}
	if got := MapDrawings(); len(got) != 5 {
		t.Fatalf("saved %d drawings, want 5: %v", len(got), got)
	}
}

func TestMapDrawEdgeBadKindRefused(t *testing.T) {
	st := useTestStore(t)
	mapDrawEdgeSeed(t, st)
	for _, k := range []string{"", "Pen", "circle", "erase", "LINE"} {
		d := mapDrawEdgeLine()
		d.Kind = k
		mapDrawEdgeRefused(t, st, "kind "+fmt.Sprintf("%q", k), map[string]MapDrawing{"x": d}, nil)
	}
}

func TestMapDrawEdgePtsLengthRefused(t *testing.T) {
	st := useTestStore(t)
	mapDrawEdgeSeed(t, st)
	nums := func(n int) []float64 { return make([]float64, n) }
	for _, tc := range []struct {
		kind string
		pts  []float64
	}{
		{"pen", nil},
		{"pen", nums(2)},    // one point
		{"pen", nums(5)},    // odd
		{"pen", nums(1002)}, // 501 points
		{"line", nums(2)},
		{"line", nums(6)},
		{"rect", nums(3)},
		{"rect", nums(8)},
		{"ellipse", nums(0)},
		{"ellipse", nums(2)},
		{"text", nums(0)},
		{"text", nums(4)},
		{"text", nil},
	} {
		d := mapDrawEdgeOf(tc.kind)
		d.Pts = tc.pts
		mapDrawEdgeRefused(t, st, fmt.Sprintf("%s with %d numbers", tc.kind, len(tc.pts)), map[string]MapDrawing{"x": d}, nil)
	}
}

func TestMapDrawEdgePenPointBoundsAccepted(t *testing.T) {
	useTestStore(t)
	two := mapDrawEdgeOf("pen")
	two.Pts = []float64{0, 0, 1, 1}
	max := mapDrawEdgeOf("pen")
	max.Pts = make([]float64, 1000)
	for i := range max.Pts {
		max.Pts[i] = float64(i)
	}
	if err := SetMapDrawings(map[string]MapDrawing{"two": two, "max": max}, nil); err != nil {
		t.Fatalf("pen with 2 and with 500 points refused: %v", err)
	}
	if got := MapDrawings()["max"]; len(got.Pts) != 1000 {
		t.Fatalf("500-point pen came back with %d numbers, want 1000", len(got.Pts))
	}
}

func TestMapDrawEdgeCoordinateBounds(t *testing.T) {
	st := useTestStore(t)
	edge := mapDrawEdgeLine()
	edge.Pts = []float64{10_000_000, -10_000_000, -10_000_000, 10_000_000}
	if err := SetMapDrawings(map[string]MapDrawing{"edge": edge}, nil); err != nil {
		t.Fatalf("coordinates at ±10,000,000 refused: %v", err)
	}
	for _, c := range []float64{
		math.Nextafter(10_000_000, math.Inf(1)),
		math.Nextafter(-10_000_000, math.Inf(-1)),
		10_000_001,
		math.NaN(),
		math.Inf(1),
		math.Inf(-1),
	} {
		d := mapDrawEdgeLine()
		d.Pts = []float64{0, 0, 0, c}
		mapDrawEdgeRefused(t, st, fmt.Sprintf("coordinate %v", c), map[string]MapDrawing{"x": d}, nil)
	}
}

func TestMapDrawEdgeSizeBounds(t *testing.T) {
	st := useTestStore(t)
	small := mapDrawEdgeLine()
	small.Size = 1e-9
	big := mapDrawEdgeLine()
	big.Size = 1000
	if err := SetMapDrawings(map[string]MapDrawing{"small": small, "big": big}, nil); err != nil {
		t.Fatalf("size 1e-9 and size 1000 refused: %v", err)
	}
	for _, s := range []float64{0, -1, math.Nextafter(1000, math.Inf(1)), 1001, math.NaN(), math.Inf(1), math.Inf(-1)} {
		d := mapDrawEdgeLine()
		d.Size = s
		mapDrawEdgeRefused(t, st, fmt.Sprintf("size %v", s), map[string]MapDrawing{"x": d}, nil)
	}
}

func TestMapDrawEdgeTextBounds(t *testing.T) {
	st := useTestStore(t)
	atCap := mapDrawEdgeOf("text")
	atCap.Text = strings.Repeat("é", 500) // 500 code points, 1000 bytes
	lines := mapDrawEdgeOf("text")
	lines.Text = "one\n\ntwo"
	if err := SetMapDrawings(map[string]MapDrawing{"cap": atCap, "lines": lines}, nil); err != nil {
		t.Fatalf("500-code-point text or multi-line text refused: %v", err)
	}
	if got := MapDrawings()["cap"].Text; got != atCap.Text {
		t.Fatalf("500-code-point text came back changed (%d runes)", len([]rune(got)))
	}
	for name, text := range map[string]string{
		"empty":           "",
		"spaces":          "   ",
		"whitespace mix":  " \n\t \r\n",
		"501 code points": strings.Repeat("é", 501),
	} {
		d := mapDrawEdgeOf("text")
		d.Text = text
		mapDrawEdgeRefused(t, st, "text "+name, map[string]MapDrawing{"x": d}, nil)
	}
}

func TestMapDrawEdgeNonTextWithTextRefused(t *testing.T) {
	st := useTestStore(t)
	mapDrawEdgeSeed(t, st)
	for _, k := range []string{"pen", "line", "rect", "ellipse"} {
		d := mapDrawEdgeOf(k)
		d.Text = "label"
		mapDrawEdgeRefused(t, st, k+" with text", map[string]MapDrawing{"x": d}, nil)
	}
}

// One bad drawing refuses the whole call: the good ones beside it are not saved.
func TestMapDrawEdgeOneBadDrawingRefusesTheBatch(t *testing.T) {
	st := useTestStore(t)
	mapDrawEdgeSeed(t, st)
	bad := mapDrawEdgeLine()
	bad.Size = 0
	mapDrawEdgeRefused(t, st, "good beside bad", map[string]MapDrawing{"good": mapDrawEdgeLine(), "bad": bad}, []string{"seed"})
	if _, ok := MapDrawings()["seed"]; !ok {
		t.Fatal("the refused call's remove of seed was applied")
	}
}

// --- per-call caps ----------------------------------------------------------------

func TestMapDrawEdgeSetOfExactlyAThousandAccepted(t *testing.T) {
	useTestStore(t)
	set := make(map[string]MapDrawing, 1000)
	for i := 0; i < 1000; i++ {
		set[mapDrawEdgeID(i)] = mapDrawEdgeLine()
	}
	if err := SetMapDrawings(set, nil); err != nil {
		t.Fatalf("set of 1000 on an empty row refused: %v", err)
	}
	if got := len(MapDrawings()); got != 1000 {
		t.Fatalf("saved %d drawings, want 1000", got)
	}
}

func TestMapDrawEdgeSetOverAThousandRefused(t *testing.T) {
	st := useTestStore(t)
	set := make(map[string]MapDrawing, 1001)
	for i := 0; i < 1001; i++ {
		set[mapDrawEdgeID(i)] = mapDrawEdgeLine()
	}
	mapDrawEdgeRefused(t, st, "set of 1001", set, nil)
}

func TestMapDrawEdgeRemoveOfExactlyAThousandUnsavedAccepted(t *testing.T) {
	st := useTestStore(t)
	mapDrawEdgeSeed(t, st)
	remove := make([]string, 1000)
	for i := range remove {
		remove[i] = mapDrawEdgeID(i)
	}
	if err := SetMapDrawings(nil, remove); err != nil {
		t.Fatalf("remove of 1000 unsaved ids refused: %v", err)
	}
	if _, ok := MapDrawings()["seed"]; !ok {
		t.Fatal("seed lost to a remove that never named it")
	}
}

func TestMapDrawEdgeRemoveOverAThousandRefused(t *testing.T) {
	st := useTestStore(t)
	mapDrawEdgeSeed(t, st)
	remove := make([]string, 1001)
	for i := range remove {
		remove[i] = mapDrawEdgeID(i)
	}
	remove[0] = "seed"
	mapDrawEdgeRefused(t, st, "remove of 1001", nil, remove)
}

// --- the result cap ---------------------------------------------------------------

func TestMapDrawEdgeFullRowNewIDRefusedNaming1000(t *testing.T) {
	st := useTestStore(t)
	mapDrawEdgeFill(t, st, 1000)
	before, _ := mapDrawEdgeRaw(t, st)
	err := SetMapDrawings(map[string]MapDrawing{"new": mapDrawEdgeLine()}, nil)
	if err == nil {
		t.Fatal("a 1001st drawing was accepted")
	}
	if !strings.Contains(err.Error(), "1000") {
		t.Errorf("over-cap error %q does not contain 1000", err.Error())
	}
	if after, _ := mapDrawEdgeRaw(t, st); after != before {
		t.Error("row changed on the over-cap refusal")
	}
}

func TestMapDrawEdgeFullRowReplaceAccepted(t *testing.T) {
	st := useTestStore(t)
	mapDrawEdgeFill(t, st, 1000)
	if err := SetMapDrawings(map[string]MapDrawing{mapDrawEdgeID(7): mapDrawEdgeOf("rect")}, nil); err != nil {
		t.Fatalf("replacing a saved id on a full row refused: %v", err)
	}
	got := MapDrawings()
	if len(got) != 1000 || got[mapDrawEdgeID(7)].Kind != "rect" {
		t.Fatalf("after replace: %d drawings, %s kind %q; want 1000 and rect", len(got), mapDrawEdgeID(7), got[mapDrawEdgeID(7)].Kind)
	}
}

func TestMapDrawEdgeFullRowRemoveOneSetOneAccepted(t *testing.T) {
	st := useTestStore(t)
	mapDrawEdgeFill(t, st, 1000)
	if err := SetMapDrawings(map[string]MapDrawing{"new": mapDrawEdgeLine()}, []string{mapDrawEdgeID(0)}); err != nil {
		t.Fatalf("remove one + set one on a full row refused: %v", err)
	}
	got := MapDrawings()
	if len(got) != 1000 {
		t.Fatalf("%d drawings, want 1000", len(got))
	}
	if _, ok := got["new"]; !ok {
		t.Error("new drawing not saved")
	}
	if _, ok := got[mapDrawEdgeID(0)]; ok {
		t.Error("removed drawing still saved")
	}
}

// A remove of an id that is not saved frees no slot.
func TestMapDrawEdgeFullRowUnsavedRemoveFreesNothing(t *testing.T) {
	st := useTestStore(t)
	mapDrawEdgeFill(t, st, 1000)
	mapDrawEdgeRefused(t, st, "remove unsaved + set new on a full row", map[string]MapDrawing{"new": mapDrawEdgeLine()}, []string{"not-saved"})
}

func TestMapDrawEdgeResultOverCapFromPartialRowRefused(t *testing.T) {
	st := useTestStore(t)
	mapDrawEdgeFill(t, st, 600)
	set := make(map[string]MapDrawing, 401)
	for i := 0; i < 401; i++ {
		set[fmt.Sprintf("n%04d", i)] = mapDrawEdgeLine()
	}
	mapDrawEdgeRefused(t, st, "600 saved + 401 new", set, nil)
	delete(set, "n0000")
	if err := SetMapDrawings(set, nil); err != nil {
		t.Fatalf("600 saved + 400 new refused: %v", err)
	}
}

// --- removes of unsaved ids --------------------------------------------------------

func TestMapDrawEdgeRemoveUnsavedOnNoRowSucceeds(t *testing.T) {
	useTestStore(t)
	if err := SetMapDrawings(nil, []string{"ghost"}); err != nil {
		t.Fatalf("remove of an unsaved id with no row: %v", err)
	}
	if got := MapDrawings(); len(got) != 0 {
		t.Fatalf("drawings after a no-op remove = %v, want none", got)
	}
}

func TestMapDrawEdgeRemoveUnsavedKeepsSaved(t *testing.T) {
	st := useTestStore(t)
	mapDrawEdgeSeed(t, st)
	if err := SetMapDrawings(nil, []string{"ghost"}); err != nil {
		t.Fatalf("remove of an unsaved id: %v", err)
	}
	got := MapDrawings()
	if len(got) != 1 {
		t.Fatalf("drawings = %v, want only seed", got)
	}
	if _, ok := got["seed"]; !ok {
		t.Fatal("seed lost")
	}
}

func TestMapDrawEdgeDuplicateRemoveIDsAccepted(t *testing.T) {
	st := useTestStore(t)
	mapDrawEdgeSeed(t, st)
	if err := SetMapDrawings(nil, []string{"seed", "seed"}); err != nil {
		t.Fatalf("the same id twice in remove: %v", err)
	}
	if got := MapDrawings(); len(got) != 0 {
		t.Fatalf("drawings = %v, want none", got)
	}
}

// --- a row that no longer parses -----------------------------------------------------

func TestMapDrawEdgeCorruptRowReadsEmpty(t *testing.T) {
	st := useTestStore(t)
	for _, raw := range []string{"{not json", "[1,2,3]", `"a string"`} {
		if err := st.SetKV(mapDrawingsKey, raw); err != nil {
			t.Fatal(err)
		}
		got := MapDrawings()
		if got == nil || len(got) != 0 {
			t.Errorf("row %q: MapDrawings() = %#v, want a non-nil empty map", raw, got)
		}
	}
}

func TestMapDrawEdgeCorruptRowReplacedBySet(t *testing.T) {
	st := useTestStore(t)
	for _, raw := range []string{"{not json", "[1,2,3]"} {
		if err := st.SetKV(mapDrawingsKey, raw); err != nil {
			t.Fatal(err)
		}
		if err := SetMapDrawings(map[string]MapDrawing{"fresh": mapDrawEdgeLine()}, nil); err != nil {
			t.Fatalf("row %q: set refused: %v", raw, err)
		}
		got := MapDrawings()
		if len(got) != 1 {
			t.Fatalf("row %q: drawings = %v, want only fresh", raw, got)
		}
		if _, ok := got["fresh"]; !ok {
			t.Fatalf("row %q: fresh not saved", raw)
		}
	}
}

func TestMapDrawEdgeCorruptRowReplacedByRemoveOnly(t *testing.T) {
	st := useTestStore(t)
	if err := st.SetKV(mapDrawingsKey, "{not json"); err != nil {
		t.Fatal(err)
	}
	if err := SetMapDrawings(nil, []string{"ghost"}); err != nil {
		t.Fatalf("remove-only call on a corrupt row refused: %v", err)
	}
	raw, ok := mapDrawEdgeRaw(t, st)
	if !ok {
		t.Fatal("no row after the call: the corrupt row was not replaced by the call's result")
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("row %q after the call still does not parse: %v", raw, err)
	}
	if len(parsed) != 0 {
		t.Fatalf("row %q, want an empty object", raw)
	}
}

// --- stored entries that fail V --------------------------------------------------

func TestMapDrawEdgeStoredInvalidEntriesLeftOut(t *testing.T) {
	st := useTestStore(t)
	row := `{
		"good": {"kind":"line","pts":[0,0,1,1],"size":2},
		"goodtext": {"kind":"text","pts":[3,4],"size":18,"text":"hi"},
		"badkind": {"kind":"star","pts":[0,0,1,1],"size":2},
		"zerosize": {"kind":"line","pts":[0,0,1,1],"size":0},
		"bigsize": {"kind":"line","pts":[0,0,1,1],"size":1001},
		"oddpen": {"kind":"pen","pts":[0,0,1],"size":2},
		"shortrect": {"kind":"rect","pts":[0,0],"size":2},
		"farcoord": {"kind":"line","pts":[0,0,1,20000000],"size":2},
		"blanktext": {"kind":"text","pts":[0,0],"size":18,"text":"  "},
		"notext": {"kind":"text","pts":[0,0],"size":18},
		"linetext": {"kind":"line","pts":[0,0,1,1],"size":2,"text":"x"},
		"nullval": null
	}`
	if err := st.SetKV(mapDrawingsKey, row); err != nil {
		t.Fatal(err)
	}
	got := MapDrawings()
	if len(got) != 2 {
		keys := make([]string, 0, len(got))
		for k := range got {
			keys = append(keys, k)
		}
		t.Fatalf("MapDrawings() ids = %v, want exactly [good goodtext]", keys)
	}
	if _, ok := got["good"]; !ok {
		t.Error("good left out")
	}
	if got["goodtext"].Text != "hi" {
		t.Errorf("goodtext = %+v, want text hi", got["goodtext"])
	}
}

// A non-text drawing stored with "text":"" passes V and reads back with no text.
func TestMapDrawEdgeStoredEmptyTextOnLineKept(t *testing.T) {
	st := useTestStore(t)
	if err := st.SetKV(mapDrawingsKey, `{"a":{"kind":"line","pts":[0,0,1,1],"size":2,"text":""}}`); err != nil {
		t.Fatal(err)
	}
	got, ok := MapDrawings()["a"]
	if !ok {
		t.Fatal("line with empty text left out")
	}
	if got.Text != "" {
		t.Fatalf("text = %q, want empty", got.Text)
	}
}

// --- Amendment 1: AM1, AM6 ------------------------------------------------------------

// mapDrawEdgeFillWithInvalid writes n valid drawings plus five that fail V.
func mapDrawEdgeFillWithInvalid(t *testing.T, st *store.Store, n int) {
	t.Helper()
	row := make(map[string]json.RawMessage, n+5)
	for i := 0; i < n; i++ {
		row[mapDrawEdgeID(i)] = json.RawMessage(`{"kind":"line","pts":[0,0,10,10],"size":2}`)
	}
	for k, v := range mapDrawEdgeInvalidEntries {
		row[k] = json.RawMessage(v)
	}
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV(mapDrawingsKey, string(data)); err != nil {
		t.Fatalf("SetKV: %v", err)
	}
}

var mapDrawEdgeInvalidEntries = map[string]string{
	"inv-kind": `{"kind":"star","pts":[0,0,1,1],"size":2}`,
	"inv-size": `{"kind":"line","pts":[0,0,1,1],"size":0}`,
	"inv-pts":  `{"kind":"rect","pts":[0,0],"size":2}`,
	"inv-text": `{"kind":"text","pts":[0,0],"size":18,"text":"  "}`,
	"inv-null": `null`,
}

// mapDrawEdgeRowIDs is the set of ids in the raw row.
func mapDrawEdgeRowIDs(t *testing.T, st *store.Store) map[string]bool {
	t.Helper()
	raw, ok := mapDrawEdgeRaw(t, st)
	if !ok {
		t.Fatal("no row")
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		t.Fatalf("row %q does not parse: %v", raw, err)
	}
	out := make(map[string]bool, len(parsed))
	for k := range parsed {
		out[k] = true
	}
	return out
}

// AM1: entries that fail V do not count toward the cap.
func TestMapDrawEdgeAM1InvalidEntriesDoNotCountTowardCap(t *testing.T) {
	st := useTestStore(t)
	mapDrawEdgeFillWithInvalid(t, st, 999)
	if err := SetMapDrawings(map[string]MapDrawing{"last": mapDrawEdgeLine()}, nil); err != nil {
		t.Fatalf("999 valid + 5 invalid stored, one new set refused: %v", err)
	}
	if got := len(MapDrawings()); got != 1000 {
		t.Fatalf("MapDrawings() holds %d, want 1000", got)
	}
}

// AM1: with 1000 valid entries stored, invalid ones beside them do not make room.
func TestMapDrawEdgeAM1InvalidEntriesFreeNoSlot(t *testing.T) {
	st := useTestStore(t)
	mapDrawEdgeFillWithInvalid(t, st, 1000)
	before, _ := mapDrawEdgeRaw(t, st)
	err := SetMapDrawings(map[string]MapDrawing{"new": mapDrawEdgeLine()}, []string{"inv-kind"})
	if err == nil {
		t.Fatal("a 1001st valid drawing was accepted after removing only an invalid entry")
	}
	if !strings.Contains(err.Error(), "1000") {
		t.Errorf("over-cap error %q does not contain 1000", err.Error())
	}
	if after, _ := mapDrawEdgeRaw(t, st); after != before {
		t.Error("row changed on the refusal")
	}
}

// AM1: the next successful set drops invalid entries from the row.
func TestMapDrawEdgeAM1SuccessfulSetDropsInvalidEntries(t *testing.T) {
	st := useTestStore(t)
	mapDrawEdgeFillWithInvalid(t, st, 2)
	if err := SetMapDrawings(map[string]MapDrawing{"new": mapDrawEdgeLine()}, nil); err != nil {
		t.Fatalf("set: %v", err)
	}
	ids := mapDrawEdgeRowIDs(t, st)
	for k := range mapDrawEdgeInvalidEntries {
		if ids[k] {
			t.Errorf("invalid entry %s still in the row after a successful set", k)
		}
	}
	for _, k := range []string{mapDrawEdgeID(0), mapDrawEdgeID(1), "new"} {
		if !ids[k] {
			t.Errorf("valid entry %s missing from the row", k)
		}
	}
}

// AM1: a successful call whose only content is a no-op remove also drops them.
func TestMapDrawEdgeAM1NoOpRemoveDropsInvalidEntries(t *testing.T) {
	st := useTestStore(t)
	mapDrawEdgeFillWithInvalid(t, st, 1)
	if err := SetMapDrawings(nil, []string{"ghost"}); err != nil {
		t.Fatalf("no-op remove: %v", err)
	}
	ids := mapDrawEdgeRowIDs(t, st)
	if len(ids) != 1 || !ids[mapDrawEdgeID(0)] {
		t.Fatalf("row ids = %v, want only %s", ids, mapDrawEdgeID(0))
	}
}

// AM1: a refused call leaves invalid entries where they are (nothing changed).
func TestMapDrawEdgeAM1RefusedCallKeepsInvalidEntries(t *testing.T) {
	st := useTestStore(t)
	mapDrawEdgeFillWithInvalid(t, st, 1)
	mapDrawEdgeRefused(t, st, "bad size beside invalid stored entries", map[string]MapDrawing{"x": {Kind: "line", Pts: []float64{0, 0, 1, 1}, Size: 0}}, nil)
}

// AM6: the client's MAX_DRAWINGS mirrors this; the server's cap stays 1000.
func TestMapDrawEdgeAM6CapIsAThousand(t *testing.T) {
	if mapDrawingsMax != 1000 {
		t.Fatalf("mapDrawingsMax = %d, want 1000", mapDrawingsMax)
	}
}

// --- serialization ----------------------------------------------------------------

// With 999 saved, two calls each adding a different new id race for the last
// slot: exactly one wins, the other is refused, and the row holds 1000.
func TestMapDrawEdgeRaceForTheLastSlot(t *testing.T) {
	st := useTestStore(t)
	mapDrawEdgeFill(t, st, 999)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = SetMapDrawings(map[string]MapDrawing{fmt.Sprintf("racer%d", i): mapDrawEdgeLine()}, nil)
		}(i)
	}
	wg.Wait()
	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("%d of 2 racers succeeded (%v), want exactly 1", wins, errs)
	}
	if got := len(MapDrawings()); got != 1000 {
		t.Fatalf("row holds %d drawings, want 1000", got)
	}
}

// Removes of unsaved ids racing with sets drop nobody's set.
func TestMapDrawEdgeConcurrentNoOpRemovesKeepSets(t *testing.T) {
	useTestStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			if err := SetMapDrawings(map[string]MapDrawing{fmt.Sprintf("s%02d", i): mapDrawEdgeLine()}, nil); err != nil {
				t.Errorf("set %d: %v", i, err)
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			if err := SetMapDrawings(nil, []string{fmt.Sprintf("ghost%02d", i)}); err != nil {
				t.Errorf("remove %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if got := len(MapDrawings()); got != 16 {
		t.Fatalf("row holds %d drawings, want 16", got)
	}
}
