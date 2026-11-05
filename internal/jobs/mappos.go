package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"

	"kraken/internal/store"
	"kraken/internal/util"
)

// mappos.go — where the operator left each card on the agent map. One kv row,
// `map_positions`, holding a JSON object from the board's own card key to
// that card's position, so the layout follows the operator across browsers
// and the Mac app. The keys are the client's (jobKey in the SPA) and opaque
// here: the server validates their size and the coordinates' range, and
// nothing else.
//
// Each stored value is `[x, y, seq]`: seq is the key's last write, a counter
// one past the row's highest, so the row itself records which keys were
// written longest ago. A value of `[x, y]` (a row saved before seq existed)
// counts as written before every stamped key. When a save would leave more
// than mapPositionsMax keys, the keys written longest ago that the save does
// not touch are dropped: a card that left the board long ago gives up its
// spot, and a save is never refused for the row being full.

// mapPositionsKey is the kv row.
const mapPositionsKey = "map_positions"

// mapPositionsMax bounds the saved keys; mapPositionKeyMax bounds one key;
// mapCoordMax bounds a coordinate's absolute value.
const (
	mapPositionsMax   = 2000
	mapPositionKeyMax = 200
	mapCoordMax       = 10_000_000
)

// mapPositionsMu serializes the read-merge-write in SetMapPositions, so two
// saves from two browsers cannot drop each other's keys.
var mapPositionsMu sync.Mutex

// savedPosition is one stored value: the position and its write stamp (0 for
// a value saved without one).
type savedPosition struct {
	X, Y float64
	Seq  float64
}

// MapPositions is every saved position: a non-nil empty map when none are
// saved, no store is wired, or the row does not parse.
func MapPositions() map[string][2]float64 {
	out := map[string][2]float64{}
	st := ActiveStore()
	if st == nil {
		return out
	}
	saved, err := readMapPositions(st)
	if err != nil {
		util.LogEvent("jobs", "error", "map-positions-read", err.Error())
		return out
	}
	for k, p := range saved {
		out[k] = [2]float64{p.X, p.Y}
	}
	return out
}

// readMapPositions reads the row; an absent row is an empty map. A value that
// is not two or three numbers is skipped rather than failing the row.
func readMapPositions(st *store.Store) (map[string]savedPosition, error) {
	out := map[string]savedPosition{}
	raw, err := st.GetKV(mapPositionsKey)
	if errors.Is(err, store.ErrNotFound) || (err == nil && raw == "") {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	var vals map[string][]float64
	if err := json.Unmarshal([]byte(raw), &vals); err != nil {
		return map[string]savedPosition{}, fmt.Errorf("parse %s: %v", mapPositionsKey, err)
	}
	for k, v := range vals {
		switch len(v) {
		case 2:
			out[k] = savedPosition{X: v[0], Y: v[1]}
		case 3:
			out[k] = savedPosition{X: v[0], Y: v[1], Seq: v[2]}
		}
	}
	return out, nil
}

// SetMapPositions merges changes into the saved positions: each key in
// changes is set (and stamped as the newest write), every other saved key
// kept. Refused, with nothing stored, when no store is wired, changes is
// empty or holds more than 2,000 keys, a key is empty or over 200 bytes, or a
// coordinate is NaN, infinite or beyond ±10,000,000. When the merge would
// leave more than 2,000 keys, the untouched keys written longest ago are
// dropped until 2,000 remain (ties broken by key, so the choice is stable). A
// saved row that no longer parses is replaced rather than refused, so a
// corrupt row cannot wedge the map for good.
func SetMapPositions(changes map[string][2]float64) error {
	st := ActiveStore()
	if st == nil {
		return fmt.Errorf("save map positions: no store is wired")
	}
	if len(changes) == 0 {
		return fmt.Errorf("save map positions: no positions given")
	}
	if len(changes) > mapPositionsMax {
		return fmt.Errorf("save map positions: more than %d positions in one save", mapPositionsMax)
	}
	for k, p := range changes {
		if k == "" {
			return fmt.Errorf("save map positions: an empty card key")
		}
		if len(k) > mapPositionKeyMax {
			return fmt.Errorf("save map positions: a card key over %d bytes", mapPositionKeyMax)
		}
		for _, c := range p {
			if math.IsNaN(c) || math.IsInf(c, 0) || math.Abs(c) > mapCoordMax {
				return fmt.Errorf("save map positions: a coordinate is not a finite number within ±%d", mapCoordMax)
			}
		}
	}
	mapPositionsMu.Lock()
	defer mapPositionsMu.Unlock()
	saved, err := readMapPositions(st)
	if err != nil {
		util.LogEvent("jobs", "error", "map-positions-read", err.Error()+" (replaced)")
		saved = map[string]savedPosition{}
	}
	seq := 0.0
	for _, p := range saved {
		if p.Seq > seq {
			seq = p.Seq
		}
	}
	seq++
	for k, p := range changes {
		saved[k] = savedPosition{X: p[0], Y: p[1], Seq: seq}
	}
	evictOldestPositions(saved, changes)
	vals := make(map[string][3]float64, len(saved))
	for k, p := range saved {
		vals[k] = [3]float64{p.X, p.Y, p.Seq}
	}
	data, err := json.Marshal(vals)
	if err != nil {
		return fmt.Errorf("save map positions: %v", err)
	}
	if err := st.SetKV(mapPositionsKey, string(data)); err != nil {
		return fmt.Errorf("save map positions: %w", err)
	}
	return nil
}

// evictOldestPositions drops the keys of saved not in keep, oldest write
// first and then by key, until at most mapPositionsMax remain.
func evictOldestPositions(saved map[string]savedPosition, keep map[string][2]float64) {
	over := len(saved) - mapPositionsMax
	if over <= 0 {
		return
	}
	cands := make([]string, 0, len(saved))
	for k := range saved {
		if _, ok := keep[k]; !ok {
			cands = append(cands, k)
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := saved[cands[i]].Seq, saved[cands[j]].Seq
		if a != b {
			return a < b
		}
		return cands[i] < cands[j]
	})
	for _, k := range cands[:over] {
		delete(saved, k)
	}
}
