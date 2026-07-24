package jobs

import (
	"sort"
)

// The board's column order is kraken-owned presentation state, kept
// separate from job files: jobs know nothing about where they render within
// a column. Store-backed (store.GetBoardOrder/SetBoardColumn), keyed by the
// five display states (draft/queued/working/blocked/done). The legacy
// ~/.kraken/order.json file this replaced is still read directly by
// `kraken import`'s one-shot migration (import.go's own defaultOrderPath).

// LoadBoardOrder returns every column's stored order. Missing, unreadable,
// or corrupt state — like every other foreign/own state file this repo
// reads — degrades to an empty map rather than an error.
func LoadBoardOrder() map[string][]string {
	columns, err := ActiveStore().GetBoardOrder()
	if err != nil {
		// Log before degrading to empty — see jobs/job.go's
		// LoadJobs for the same rationale (a DB fault must not silently
		// read as "board order was never set").
		dlogErr("load board order from store failed: %v", err)
		return map[string][]string{}
	}
	return columns
}

// SaveColumnOrder replaces one column's stored order via store.SetBoardColumn,
// which already dedupes, skips empty slugs, and enforces one-column-per-slug
// (a slug is removed from every OTHER column, not just assigned into this
// one) — a drag-and-drop firing twice, or two tabs racing a reorder of
// different columns, must not leave a slug rendered twice on the board.
func SaveColumnOrder(column string, slugs []string) error {
	return ActiveStore().SetBoardColumn(column, slugs)
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
