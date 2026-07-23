package store

import "fmt"

// GetBoardOrder returns every column's slug order, keyed by column name and
// sorted by position within each column.
func (s *Store) GetBoardOrder() (map[string][]string, error) {
	rows, err := s.db.Query(`SELECT "column", slug FROM board_order ORDER BY "column", position`)
	if err != nil {
		return nil, fmt.Errorf("get board order: %w", err)
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var col, slug string
		if err := rows.Scan(&col, &slug); err != nil {
			return nil, fmt.Errorf("get board order: %w", err)
		}
		out[col] = append(out[col], slug)
	}
	return out, rows.Err()
}

// SetBoardColumn atomically replaces one column's slug order: delete the
// column's rows, then insert slugs at sequential positions — the reorder
// primitive the board's drag-and-drop drives. slugs is deduplicated first
// (first occurrence wins, empty slugs skipped entirely — L9, matching the
// file path's SaveColumnOrder), and each surviving slug is removed from
// every other column in the same transaction, so a slug always lives in
// exactly one column — a drag-and-drop that fires twice, or races another
// client's reorder of a different column, can never leave a slug
// duplicated on the board.
func (s *Store) SetBoardColumn(column string, slugs []string) error {
	seen := make(map[string]bool, len(slugs))
	deduped := make([]string, 0, len(slugs))
	for _, slug := range slugs {
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		deduped = append(deduped, slug)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("set board column %s: %w", column, err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM board_order WHERE "column" = ?`, column); err != nil {
		return fmt.Errorf("set board column %s: %w", column, err)
	}
	for i, slug := range deduped {
		if _, err := tx.Exec(`DELETE FROM board_order WHERE "column" != ? AND slug = ?`, column, slug); err != nil {
			return fmt.Errorf("set board column %s: %w", column, err)
		}
		if _, err := tx.Exec(
			`INSERT INTO board_order ("column", position, slug) VALUES (?, ?, ?)`,
			column, i, slug,
		); err != nil {
			return fmt.Errorf("set board column %s: %w", column, err)
		}
	}
	return tx.Commit()
}
