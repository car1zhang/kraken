package store

import (
	"fmt"
)

// jobmsg.go — schema v40's job_messages table: which job's cell has messaged
// which other job's zeus through `kraken s`, and how often. One row per
// ordered (from, to) pair, written by kraken serve when the sending cell's
// `kraken s` reports the send (internal/api/jobmsg.go), and drawn by the
// board's agent map as one thread per pair of cards.
//
// Like the spawn links, nothing here bumps `rev` or stamps `updated_at` on
// either job: a message count is bookkeeping about the cells, not an edit to
// either job's text.

// JobMessage is one row. Tag-free like every type in this package: the API
// layer owns the wire shape (internal/api/activity.go's webTalk).
type JobMessage struct {
	FromID, ToID, N, FirstAt, LastAt int64
}

// BumpJobMessage counts one message from fromID to toID at `at` (unix
// millis): the first call for the pair stores n = 1 with both stamps at
// `at`, every later one adds 1 and moves last_at, leaving first_at. One
// statement, so two concurrent bumps cannot lose a count. Neither id is
// checked against `jobs`; the caller resolves both first.
func (s *Store) BumpJobMessage(fromID, toID, at int64) error {
	if _, err := s.db.Exec(
		`INSERT INTO job_messages (from_id, to_id, n, first_at, last_at) VALUES (?, ?, 1, ?, ?)
			ON CONFLICT(from_id, to_id) DO UPDATE SET n = n + 1, last_at = excluded.last_at`,
		fromID, toID, at, at,
	); err != nil {
		return fmt.Errorf("bump job message %d -> %d: %w", fromID, toID, err)
	}
	return nil
}

// ListJobMessages returns the whole table ordered by last_at, then from_id.
// The board reads it once per build and indexes it itself.
func (s *Store) ListJobMessages() ([]JobMessage, error) {
	rows, err := s.db.Query(`SELECT from_id, to_id, n, first_at, last_at FROM job_messages ORDER BY last_at, from_id, to_id`)
	if err != nil {
		return nil, fmt.Errorf("list job messages: %w", err)
	}
	defer rows.Close()
	var out []JobMessage
	for rows.Next() {
		var m JobMessage
		if err := rows.Scan(&m.FromID, &m.ToID, &m.N, &m.FirstAt, &m.LastAt); err != nil {
			return nil, fmt.Errorf("list job messages: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// deleteMessagesForJob removes every row naming id on EITHER side.
// DeleteJob's companion, for deleteSpawnsForJob's reason.
func deleteMessagesForJob(x execer, id int64) error {
	if _, err := x.Exec(`DELETE FROM job_messages WHERE from_id = ? OR to_id = ?`, id, id); err != nil {
		return fmt.Errorf("delete messages for job %d: %w", id, err)
	}
	return nil
}
