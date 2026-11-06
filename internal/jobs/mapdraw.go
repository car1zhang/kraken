package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"kraken/internal/store"
	"kraken/internal/util"
)

// mapdraw.go: the drawings the operator puts on the agent map's plane:
// freehand strokes, straight lines, rectangles, ellipses and text labels. One
// kv row, `map_drawings`, holding a JSON object from a client-minted id to
// one drawing. Drawings touch nothing else: no card, no physics, no job.
//
// The server checks every drawing it is handed against the same rule the
// board applies (validMapDrawing), so a row written through this file only
// ever holds drawings the board can draw. Unlike the saved positions, the row
// is never trimmed: a save that would leave more than mapDrawingsMax drawings
// is refused, because dropping the operator's ink to make room would lose
// something they made on purpose.

// mapDrawingsKey is the kv row.
const mapDrawingsKey = "map_drawings"

// mapDrawingsMax bounds the saved drawings, and also the ids one save may
// set or remove.
const mapDrawingsMax = 1000

// The bounds of one drawing: an id's bytes, a pen stroke's points, a text's
// code points, and the stroke width or font size. A coordinate shares
// mapCoordMax with the saved positions.
const (
	mapDrawingIDMax   = 64
	mapDrawingPenMax  = 500
	mapDrawingTextMax = 500
	mapDrawingSizeMax = 1000
)

// errMapDrawingsUnparsed marks a row that was read but does not parse, the
// one read failure SetMapDrawings may overwrite.
var errMapDrawingsUnparsed = errors.New("unparsed row")

// ErrMapDrawingsStore marks a SetMapDrawings refusal that is the store's
// fault (the row could not be read, or the write failed) rather than the
// call's, so the route can answer it with a 500 instead of a 400.
var ErrMapDrawingsStore = errors.New("store failure")

// mapDrawingsMu serializes the read-merge-write in SetMapDrawings, so two
// saves from two browsers cannot drop each other's ids.
var mapDrawingsMu sync.Mutex

// MapDrawing is one drawing as the wire carries it and the row stores it.
// Pts is a flat list of world coordinates whose length depends on Kind; Size
// is the stroke width, or a text's font size; Text is a text drawing's lines,
// separated by "\n", and empty for every other kind.
type MapDrawing struct {
	Kind string    `json:"kind"`
	Pts  []float64 `json:"pts"`
	Size float64   `json:"size"`
	Text string    `json:"text,omitempty"`
}

// validMapDrawing says why d is not a drawing the board can draw, or nil.
func validMapDrawing(d MapDrawing) error {
	n := len(d.Pts)
	switch d.Kind {
	case "pen":
		if n < 4 || n > 2*mapDrawingPenMax || n%2 != 0 {
			return fmt.Errorf("a pen stroke is 2 to %d points", mapDrawingPenMax)
		}
	case "line", "rect", "ellipse":
		if n != 4 {
			return fmt.Errorf("a %s is two points", d.Kind)
		}
	case "text":
		if n != 2 {
			return fmt.Errorf("a text is one point")
		}
	default:
		return fmt.Errorf("a drawing's kind is pen, line, rect, ellipse or text")
	}
	for _, c := range d.Pts {
		if math.IsNaN(c) || math.IsInf(c, 0) || math.Abs(c) > mapCoordMax {
			return fmt.Errorf("a coordinate is not a finite number within ±%d", mapCoordMax)
		}
	}
	if math.IsNaN(d.Size) || math.IsInf(d.Size, 0) || d.Size <= 0 || d.Size > mapDrawingSizeMax {
		return fmt.Errorf("a drawing's size is a number above 0 and at most %d", mapDrawingSizeMax)
	}
	if d.Kind != "text" {
		if d.Text != "" {
			return fmt.Errorf("only a text drawing carries text")
		}
		return nil
	}
	if strings.IndexFunc(d.Text, func(r rune) bool { return !mapDrawingBlank(r) }) < 0 {
		return fmt.Errorf("a text drawing needs some text")
	}
	if utf8.RuneCountInString(d.Text) > mapDrawingTextMax {
		return fmt.Errorf("a text drawing is at most %d characters", mapDrawingTextMax)
	}
	return nil
}

// mapDrawingBlank is the whitespace of the text rule: every code point with
// the Unicode White_Space property (unicode.IsSpace) plus U+FEFF. The board's
// drawings.ts judges a text by the same set, so a text it accepts is never
// refused here and then held in its save queue.
func mapDrawingBlank(r rune) bool {
	return unicode.IsSpace(r) || r == 0xFEFF
}

// validMapDrawingID says why id cannot name a drawing, or nil.
func validMapDrawingID(id string) error {
	if id == "" {
		return fmt.Errorf("an empty drawing id")
	}
	if len(id) > mapDrawingIDMax {
		return fmt.Errorf("a drawing id over %d bytes", mapDrawingIDMax)
	}
	return nil
}

// MapDrawings is every saved drawing by id: a non-nil empty map when none
// are saved, no store is wired, or the row does not parse.
func MapDrawings() map[string]MapDrawing {
	st := ActiveStore()
	if st == nil {
		return map[string]MapDrawing{}
	}
	saved, err := readMapDrawings(st)
	if err != nil {
		util.LogEvent("jobs", "error", "map-drawings-read", err.Error())
		return map[string]MapDrawing{}
	}
	return saved
}

// readMapDrawings reads the row; an absent row is an empty map. Each entry is
// decoded on its own, and one that does not decode or fails validMapDrawing
// is skipped rather than failing the row, so a single bad entry cannot hide
// the rest. A row that is read but does not parse returns an error wrapping
// errMapDrawingsUnparsed; any other error is the store's.
func readMapDrawings(st *store.Store) (map[string]MapDrawing, error) {
	out := map[string]MapDrawing{}
	raw, err := st.GetKV(mapDrawingsKey)
	if errors.Is(err, store.ErrNotFound) || (err == nil && raw == "") {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	var vals map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &vals); err != nil {
		return map[string]MapDrawing{}, fmt.Errorf("parse %s: %w: %v", mapDrawingsKey, errMapDrawingsUnparsed, err)
	}
	for id, v := range vals {
		var d MapDrawing
		if json.Unmarshal(v, &d) != nil || validMapDrawing(d) != nil {
			continue
		}
		out[id] = d
	}
	return out, nil
}

// SetMapDrawings applies remove and then set to the saved drawings and
// writes the row once: each id in set is saved (replacing a drawing already
// under it), each id in remove deleted (a no-op when it is not saved), and
// every other saved id kept. Refused, with nothing stored, when no store is
// wired, set and remove are both empty, an id is empty or over 64 bytes, an
// id is in both, a drawing fails validMapDrawing, either holds more than
// 1,000 ids, or the result would hold more than 1,000 drawings. The cap is
// checked after the removes, so a full map can still trade one drawing for
// another. A saved row that no longer parses is replaced rather than
// refused, so a corrupt row cannot wedge the drawings for good; an entry the
// read skipped as invalid is dropped from the row by the same write. A row
// the store fails to read at all is refused instead: the row may be intact,
// and replacing it would delete every drawing it holds. That refusal and a
// failed write wrap ErrMapDrawingsStore.
func SetMapDrawings(set map[string]MapDrawing, remove []string) error {
	st := ActiveStore()
	if st == nil {
		return fmt.Errorf("save map drawings: no store is wired")
	}
	if len(set) == 0 && len(remove) == 0 {
		return fmt.Errorf("save map drawings: no drawings to set or remove")
	}
	if len(set) > mapDrawingsMax || len(remove) > mapDrawingsMax {
		return fmt.Errorf("save map drawings: more than %d ids in one save", mapDrawingsMax)
	}
	removing := make(map[string]bool, len(remove))
	for _, id := range remove {
		if err := validMapDrawingID(id); err != nil {
			return fmt.Errorf("save map drawings: %v", err)
		}
		removing[id] = true
	}
	for id, d := range set {
		if err := validMapDrawingID(id); err != nil {
			return fmt.Errorf("save map drawings: %v", err)
		}
		if removing[id] {
			return fmt.Errorf("save map drawings: drawing %q is both set and removed", id)
		}
		if err := validMapDrawing(d); err != nil {
			return fmt.Errorf("save map drawings: drawing %q: %v", id, err)
		}
	}
	mapDrawingsMu.Lock()
	defer mapDrawingsMu.Unlock()
	saved, err := readMapDrawings(st)
	if errors.Is(err, errMapDrawingsUnparsed) {
		util.LogEvent("jobs", "error", "map-drawings-read", err.Error()+" (replaced)")
		saved = map[string]MapDrawing{}
	} else if err != nil {
		return fmt.Errorf("save map drawings: %w: %w", ErrMapDrawingsStore, err)
	}
	for id := range removing {
		delete(saved, id)
	}
	for id, d := range set {
		saved[id] = d
	}
	if len(saved) > mapDrawingsMax {
		return fmt.Errorf("save map drawings: the map holds at most %d drawings", mapDrawingsMax)
	}
	data, err := json.Marshal(saved)
	if err != nil {
		return fmt.Errorf("save map drawings: %v", err)
	}
	if err := st.SetKV(mapDrawingsKey, string(data)); err != nil {
		return fmt.Errorf("save map drawings: %w: %w", ErrMapDrawingsStore, err)
	}
	return nil
}
