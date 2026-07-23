package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"kraken/util"
)

// The board's column order is kraken-owned presentation state, kept
// separate from job files: jobs know nothing about where they render within
// a column. Stored at ~/.kraken/order.json as {"columns": {state: [slug,
// ...]}}, keyed by the five display states (draft/queued/working/blocked/
// done).

func orderPath() string {
	return filepath.Join(util.Home(), ".kraken", "order.json")
}

type boardOrder struct {
	Columns map[string][]string `json:"columns"`
}

// LoadBoardOrder returns every column's stored order: store-backed
// (store.GetBoardOrder) when a store is wired, else ~/.kraken/order.json.
// Missing, unreadable, or corrupt state — like every other foreign/own
// state file this repo reads — degrades to an empty map rather than an
// error.
func LoadBoardOrder() map[string][]string {
	if st := ActiveStore(); st != nil {
		columns, err := st.GetBoardOrder()
		if err != nil {
			// Log before degrading to empty — see jobs/job.go's
			// LoadJobs for the same rationale (a DB fault must not silently
			// read as "board order was never set").
			dlogErr("load board order from store failed: %v", err)
			return map[string][]string{}
		}
		return columns
	}
	data, err := os.ReadFile(orderPath())
	if err != nil {
		return map[string][]string{}
	}
	var o boardOrder
	if err := json.Unmarshal(data, &o); err != nil || o.Columns == nil {
		return map[string][]string{}
	}
	return o.Columns
}

// SaveColumnOrder replaces one column's stored order: store-backed
// (store.SetBoardColumn, which already dedupes, skips empty slugs, and
// enforces one-column-per-slug) when a store is wired, else the file
// written back atomically. L9: the file path mirrors that SAME
// one-column-per-slug invariant — every surviving slug is removed from
// every OTHER column, not just assigned into this one — so a slug can
// never end up duplicated across two columns in order.json the way it
// already can't in the store (a drag-and-drop firing twice, or two tabs
// racing a reorder of different columns, must not leave a slug rendered
// twice on the board).
func SaveColumnOrder(column string, slugs []string) error {
	if st := ActiveStore(); st != nil {
		return st.SetBoardColumn(column, slugs)
	}
	columns := LoadBoardOrder()

	seen := make(map[string]bool, len(slugs))
	deduped := make([]string, 0, len(slugs))
	for _, s := range slugs {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		deduped = append(deduped, s)
	}
	for other, members := range columns {
		if other == column {
			continue
		}
		kept := members[:0:0]
		for _, m := range members {
			if !seen[m] {
				kept = append(kept, m)
			}
		}
		columns[other] = kept
	}
	columns[column] = deduped

	data, err := json.MarshalIndent(boardOrder{Columns: columns}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(orderPath()), 0o755); err != nil {
		return err
	}
	return util.AtomicWrite(orderPath(), string(data))
}

// ApplyColumnOrder renders a column's display order: members is the current
// set of slugs in that column, already in slug-alphabetical order (LoadJobs
// guarantees this). Members absent from stored come first, in their given
// (alphabetical) order — new/arrived jobs land on top — followed by members
// present in stored, in stored's order. Stale stored entries (no longer a
// member) are ignored here; they're only pruned the next time this column
// is saved.
func ApplyColumnOrder(members []string, stored []string) []string {
	position := make(map[string]int, len(stored))
	for i, s := range stored {
		if _, exists := position[s]; !exists {
			position[s] = i
		}
	}

	var unlisted, listed []string
	for _, m := range members {
		if _, ok := position[m]; ok {
			listed = append(listed, m)
		} else {
			unlisted = append(unlisted, m)
		}
	}
	sort.SliceStable(listed, func(i, k int) bool {
		return position[listed[i]] < position[listed[k]]
	})

	out := make([]string, 0, len(members))
	out = append(out, unlisted...)
	out = append(out, listed...)
	return out
}
