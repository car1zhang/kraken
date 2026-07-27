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
//
// THE SLUG/ID BOUNDARY LIVES HERE. Since schema v11 the board_order table
// keys on jobs.id, but /api/reorder's payload is — and stays — a list of
// SLUGS, because that is what the browser's `data-slug` attributes carry and
// what a human reads. So these two functions are the translation: slugs in,
// ids to the store, ids back, slugs out. Nothing above this file (api/board.go,
// api/actions.go, web/static/app.js, the iOS client) changes at all, and
// nothing below it ever sees a slug again.
//
// Translation is by ACTIVE job only, which is what makes it unambiguous:
// jobs_active_slug guarantees at most one active job per slug, and a column of
// the board only ever holds active jobs. An archived job is not a member of any
// column and never needs a position.

// LoadBoardOrder returns every column's stored order, translated back to
// slugs. Missing, unreadable, or corrupt state — like every other foreign/own
// state file this repo reads — degrades to an empty map rather than an error.
//
// An id with no matching active job is DROPPED from the returned order rather
// than rendered as an empty string: it is a job that was archived or deleted
// since the column was last saved, so it is not a member and ApplyColumnOrder
// would ignore it anyway. The stale row itself stays in the table until that
// column is next saved (store/board_order.go explains why it must not cascade
// away instead).
func LoadBoardOrder() map[string][]string {
	st := ActiveStore()
	if st == nil {
		return map[string][]string{}
	}
	columns, err := st.GetBoardOrder()
	if err != nil {
		// Log before degrading to empty — see jobs/job.go's
		// LoadJobs for the same rationale (a DB fault must not silently
		// read as "board order was never set").
		dlogErr("load board order from store failed: %v", err)
		return map[string][]string{}
	}
	rows, err := st.ListJobs()
	if err != nil {
		dlogErr("load board order: list jobs for id->slug: %v", err)
		return map[string][]string{}
	}
	slugByID := make(map[int64]string, len(rows))
	for _, r := range rows {
		slugByID[r.ID] = r.Slug
	}

	out := make(map[string][]string, len(columns))
	for col, ids := range columns {
		slugs := make([]string, 0, len(ids))
		for _, id := range ids {
			if slug, ok := slugByID[id]; ok {
				slugs = append(slugs, slug)
			}
		}
		if len(slugs) > 0 {
			out[col] = slugs
		}
	}
	return out
}

// SaveColumnOrder replaces one column's stored order, translating the caller's
// slugs to job ids on the way in. store.SetBoardColumn already dedupes, skips
// zero ids, and enforces one-column-per-job (a job is removed from every OTHER
// column, not just assigned into this one) — a drag-and-drop firing twice, or
// two tabs racing a reorder of different columns, must not leave a card
// rendered twice on the board.
//
// A slug naming no active job is dropped here rather than passed down as a
// zero: the store's skip would swallow it identically, but doing it here keeps
// "a slug is only meaningful against the active namespace" as a fact about this
// boundary rather than a coincidence of the layer below.
func SaveColumnOrder(column string, slugs []string) error {
	st := ActiveStore()
	if st == nil {
		return nil
	}
	rows, err := st.ListJobs()
	if err != nil {
		return err
	}
	idBySlug := make(map[string]int64, len(rows))
	for _, r := range rows {
		idBySlug[r.Slug] = r.ID
	}
	ids := make([]int64, 0, len(slugs))
	for _, s := range slugs {
		if id, ok := idBySlug[s]; ok {
			ids = append(ids, id)
		}
	}
	return st.SetBoardColumn(column, ids)
}

// ApplyColumnOrder renders a column's display order: members is the current
// set of slugs in that column, already in slug-alphabetical order (LoadJobs
// guarantees this). Members absent from stored come first, in their given
// (alphabetical) order — new/arrived jobs land on top — followed by members
// present in stored, in stored's order. Stale stored entries (no longer a
// member) are ignored here; they're only pruned the next time this column
// is saved.
//
// Deliberately still in SLUGS, and deliberately still pure. It is the one
// piece of this file with no store access, it is called from api/board.go
// against the slugs already on the wire, and the ambiguity that motivated the
// v11 re-key cannot reach it: its input is one column of the ACTIVE board, so
// its slugs are unique by construction.
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
