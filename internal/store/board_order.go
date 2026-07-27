package store

import "fmt"

// board_order.go — the board's manual within-column ordering, keyed on
// jobs.id since schema v11.
//
// The id is not cosmetic here. The table's only job is to say "this card sits
// above that one", and before v11 it said so with a slug — which is unique
// only among ACTIVE jobs and is FREED by archiving. SetBoardColumn's
// cross-column dedupe below deletes the dragged key from every other column,
// so with an archived and an active job sharing a slug, dragging one card
// evicted the other's saved position. There is no FK and no cascade: an entry
// naming a job that no longer exists must go STALE, not vanish — it is simply
// not a member of any column's render (jobs/order.go's ApplyColumnOrder
// ignores it) and is pruned the next time that column is saved, whereas a
// cascade firing mid-drag would reshuffle the board under carl's cursor.

// GetBoardOrder returns every column's job-id order, keyed by column name and
// sorted by position within each column.
func (s *Store) GetBoardOrder() (map[string][]int64, error) {
	rows, err := s.db.Query(`SELECT "column", job_id FROM board_order ORDER BY "column", position`)
	if err != nil {
		return nil, fmt.Errorf("get board order: %w", err)
	}
	defer rows.Close()
	out := map[string][]int64{}
	for rows.Next() {
		var col string
		var id int64
		if err := rows.Scan(&col, &id); err != nil {
			return nil, fmt.Errorf("get board order: %w", err)
		}
		out[col] = append(out[col], id)
	}
	return out, rows.Err()
}

// SetBoardColumn atomically replaces one column's job-id order: delete the
// column's rows, then insert ids at sequential positions — the reorder
// primitive the board's drag-and-drop drives. ids is deduplicated first
// (first occurrence wins, zero ids skipped entirely — L9, matching the file
// path's SaveColumnOrder, with 0 taking over the empty string's role as "not a
// job"), and each surviving id is removed from every other column in the same
// transaction, so a job always lives in exactly one column — a drag-and-drop
// that fires twice, or races another client's reorder of a different column,
// can never leave a card duplicated on the board.
//
// That cross-column DELETE is the statement v11's re-key actually fixes: it
// used to match on the slug, so with two same-slug jobs it evicted the OTHER
// job's saved position as a side effect of dragging this one.
func (s *Store) SetBoardColumn(column string, ids []int64) error {
	seen := make(map[int64]bool, len(ids))
	deduped := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		deduped = append(deduped, id)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("set board column %s: %w", column, err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM board_order WHERE "column" = ?`, column); err != nil {
		return fmt.Errorf("set board column %s: %w", column, err)
	}
	for i, id := range deduped {
		if _, err := tx.Exec(`DELETE FROM board_order WHERE "column" != ? AND job_id = ?`, column, id); err != nil {
			return fmt.Errorf("set board column %s: %w", column, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO board_order ("column", position, job_id) VALUES (?, ?, ?)`,
			column, i, id,
		); err != nil {
			return fmt.Errorf("set board column %s: %w", column, err)
		}
	}
	return tx.Commit()
}
