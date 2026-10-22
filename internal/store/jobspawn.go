package store

import (
	"fmt"
)

// jobspawn.go — schema v38's job_spawns table: which job's cell filed which
// other job. One row per CHILD, written by kraken serve when a cell's own
// `kraken job draft` or `kraken cell submit` arrives carrying the sending
// cell's slug (internal/api/spawn.go), and rendered by the board as one chip
// per child on the parent and one parent chip on the child.
//
// Like the stack-link and account writers, nothing here bumps `rev` or stamps
// `updated_at` on either job: the link is bookkeeping about who filed whom,
// not an edit to either job's text, and the table is separate from `jobs`
// precisely so the rows never pass through the job-fields path.

// SpawnLink is one row. Tag-free like every type in this package: the API
// layer owns the wire shape (internal/api/board.go's webSpawnLink).
type SpawnLink struct {
	ChildID  int64
	ParentID int64
	At       int64
	Via      string
}

// SetJobSpawn records parent as child's filer unless child already has one:
// INSERT OR IGNORE on the primary key, so the first writer wins and a later
// submit of a child the draft already stamped changes nothing. Returns true
// when a row was inserted. Neither id is checked against `jobs` — the API
// resolves both before calling, and a row for an id that later disappears is
// dropped at render time rather than refused here.
func (s *Store) SetJobSpawn(childID, parentID, at int64, via string) (bool, error) {
	res, err := s.db.Exec(
		`INSERT OR IGNORE INTO job_spawns (child_id, parent_id, at, via) VALUES (?, ?, ?, ?)`,
		childID, parentID, at, via,
	)
	if err != nil {
		return false, fmt.Errorf("set job spawn %d <- %d: %w", childID, parentID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("set job spawn %d <- %d: %w", childID, parentID, err)
	}
	return n > 0, nil
}

// ListJobSpawns returns the whole table in stamp order (then child id, so two
// children stamped in the same millisecond keep a stable order). The board
// reads it once per build and indexes both directions itself; a per-job
// query here would be one more query per card.
func (s *Store) ListJobSpawns() ([]SpawnLink, error) {
	rows, err := s.db.Query(`SELECT child_id, parent_id, at, via FROM job_spawns ORDER BY at, child_id`)
	if err != nil {
		return nil, fmt.Errorf("list job spawns: %w", err)
	}
	defer rows.Close()
	var out []SpawnLink
	for rows.Next() {
		var l SpawnLink
		if err := rows.Scan(&l.ChildID, &l.ParentID, &l.At, &l.Via); err != nil {
			return nil, fmt.Errorf("list job spawns: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// deleteSpawnsForJob removes every row naming id on EITHER side. DeleteJob's
// companion: a hard-deleted job is gone from active and archived alike, so a
// link to or from it would render as nothing anyway, and the row is noise.
func deleteSpawnsForJob(x execer, id int64) error {
	if _, err := x.Exec(`DELETE FROM job_spawns WHERE child_id = ? OR parent_id = ?`, id, id); err != nil {
		return fmt.Errorf("delete spawns for job %d: %w", id, err)
	}
	return nil
}
