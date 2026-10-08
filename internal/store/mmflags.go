package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// mmflags.go — schema v33's mm_flags (schema.go): one row per contradiction an
// agent noticed between a mental-model item and another item, or between an
// item and what it was looking at.
//
// A flag moves through two hands, and the states say whose move it is. An
// agent files it `pending` and carries on; zeus then either RAISES it to carl
// (`raised`) or DISMISSES it as noise (`dismissed`) — both terminal for zeus;
// and a raised flag waits for carl's ruling, `drop` (the item is wrong) or
// `keep` (it stands), recorded once in resolution/resolved_at and moving the
// flag to `resolved` — so `raised` alone means "waiting on carl". Every verb
// below moves exactly one step and refuses any other, so a second raise, a
// resolve of a pending flag, or a re-ruling cannot quietly rewrite what was
// decided.
//
// No FK and no cascade, the mm durability posture (mm.go's header): ItemID
// and AgainstID name items by id only, and a flag outlives its job.

// MMFlag is one mm_flags row. AgainstID is 0 when the contradiction is with
// the transcript rather than with another item. RaisedBy/RaiseNote/RaisedAt
// record whichever of raise or dismiss happened — State says which.
type MMFlag struct {
	ID, ItemID, AgainstID, JobID                                int64
	Slug, Session, Note, State, RaisedBy, RaiseNote, Resolution string
	CreatedAt, RaisedAt, ResolvedAt                             int64
}

// validMMFlagState / validMMFlagResolution mirror schemaV33's CHECKs, so a bad
// value is refused with a sentence naming the accepted set (mm.go's idiom).
var (
	validMMFlagState      = map[string]bool{"pending": true, "raised": true, "dismissed": true, "resolved": true}
	validMMFlagResolution = map[string]bool{"drop": true, "keep": true}
)

const mmFlagColumns = `SELECT id, COALESCE(item_id,0), COALESCE(against_id,0), COALESCE(job_id,0),
	COALESCE(slug,''), COALESCE(session,''), COALESCE(note,''), COALESCE(state,''),
	COALESCE(raised_by,''), COALESCE(raise_note,''), COALESCE(resolution,''),
	COALESCE(created_at,0), COALESCE(raised_at,0), COALESCE(resolved_at,0)
	FROM mm_flags`

func scanMMFlag(row interface{ Scan(...any) error }) (MMFlag, error) {
	var f MMFlag
	err := row.Scan(&f.ID, &f.ItemID, &f.AgainstID, &f.JobID, &f.Slug, &f.Session, &f.Note,
		&f.State, &f.RaisedBy, &f.RaiseNote, &f.Resolution, &f.CreatedAt, &f.RaisedAt, &f.ResolvedAt)
	return f, err
}

// AddMMFlag stores one flag and returns its id. State is stored as given,
// `pending` when empty; a state outside the three, or an empty note, is
// refused before the INSERT. A zero CreatedAt is stamped to now, so a caller
// filing a flag mid-turn need not carry a clock.
func (s *Store) AddMMFlag(f MMFlag) (int64, error) {
	if strings.TrimSpace(f.Note) == "" {
		return 0, errors.New("mm flag note is empty")
	}
	if f.State == "" {
		f.State = "pending"
	}
	if !validMMFlagState[f.State] {
		return 0, fmt.Errorf("bad mm flag state %q: want pending, raised, dismissed, or resolved", f.State)
	}
	if f.Resolution != "" && !validMMFlagResolution[f.Resolution] {
		return 0, fmt.Errorf("bad mm flag resolution %q: want drop or keep", f.Resolution)
	}
	if f.CreatedAt == 0 {
		f.CreatedAt = time.Now().UnixMilli()
	}
	res, err := s.db.Exec(
		`INSERT INTO mm_flags (item_id, against_id, job_id, slug, session, note, state,
			raised_by, raise_note, raised_at, resolved_at, resolution, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		f.ItemID, f.AgainstID, f.JobID, f.Slug, f.Session, f.Note, f.State,
		f.RaisedBy, f.RaiseNote, f.RaisedAt, f.ResolvedAt, f.Resolution, f.CreatedAt,
	)
	if err != nil {
		return 0, fmt.Errorf("add mm flag on item %d: %w", f.ItemID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("add mm flag on item %d: %w", f.ItemID, err)
	}
	return id, nil
}

// GetMMFlag reads one flag — ErrNotFound when the id names nothing.
func (s *Store) GetMMFlag(id int64) (MMFlag, error) {
	f, err := scanMMFlag(s.db.QueryRow(mmFlagColumns+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return MMFlag{}, fmt.Errorf("get mm flag %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return MMFlag{}, fmt.Errorf("get mm flag %d: %w", id, err)
	}
	return f, nil
}

// ListMMFlags returns flags newest first (created_at, then id, ListMMItems'
// tiebreak). An empty state lists every flag; any other value filters
// exactly, so a state outside the three simply matches nothing.
func (s *Store) ListMMFlags(state string) ([]MMFlag, error) {
	query := mmFlagColumns
	var args []any
	if state != "" {
		query += ` WHERE state = ?`
		args = append(args, state)
	}
	query += ` ORDER BY created_at DESC, id DESC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list mm flags: %w", err)
	}
	defer rows.Close()
	out := []MMFlag{}
	for rows.Next() {
		f, err := scanMMFlag(rows)
		if err != nil {
			return nil, fmt.Errorf("list mm flags: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list mm flags: %w", err)
	}
	return out, nil
}

// RaiseMMFlag moves a pending flag to raised: zeus is putting it in front of
// carl.
func (s *Store) RaiseMMFlag(id, at int64, by, note string) error {
	return s.decideMMFlag(id, "raised", at, by, note)
}

// DismissMMFlag moves a pending flag to dismissed: zeus judged it noise. The
// same columns record the dismissal as would have recorded a raise.
func (s *Store) DismissMMFlag(id, at int64, by, note string) error {
	return s.decideMMFlag(id, "dismissed", at, by, note)
}

// decideMMFlag is zeus' one move on a flag. The state guard sits in the
// UPDATE's WHERE, so the check and the write are one statement and a flag
// that is not pending is left exactly as it was.
func (s *Store) decideMMFlag(id int64, to string, at int64, by, note string) error {
	res, err := s.db.Exec(
		`UPDATE mm_flags SET state = ?, raised_by = ?, raise_note = ?, raised_at = ?
			WHERE id = ? AND state = 'pending'`,
		to, by, note, at, id,
	)
	if err != nil {
		return fmt.Errorf("mark mm flag %d %s: %w", id, to, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark mm flag %d %s: %w", id, to, err)
	}
	if n > 0 {
		return nil
	}
	f, err := s.GetMMFlag(id)
	if err != nil {
		return fmt.Errorf("mark mm flag %d %s: %w", id, to, err)
	}
	return fmt.Errorf("mark mm flag %d %s: the flag is %s, and only a pending flag can be raised or dismissed", id, to, f.State)
}

// ResolveMMFlag records carl's ruling on a raised flag: `drop` or `keep`,
// once, moving the flag to `resolved` so ListMMFlags("raised") lists only the
// flags still waiting on him. A flag that is pending, dismissed, or already
// ruled on is refused
// with nothing changed — the guard is in the UPDATE's WHERE for
// decideMMFlag's reason.
func (s *Store) ResolveMMFlag(id, at int64, resolution string) error {
	if !validMMFlagResolution[resolution] {
		return fmt.Errorf("resolve mm flag %d: bad resolution %q: want drop or keep", id, resolution)
	}
	res, err := s.db.Exec(
		`UPDATE mm_flags SET state = 'resolved', resolution = ?, resolved_at = ?
			WHERE id = ? AND state = 'raised' AND resolution = ''`,
		resolution, at, id,
	)
	if err != nil {
		return fmt.Errorf("resolve mm flag %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("resolve mm flag %d: %w", id, err)
	}
	if n > 0 {
		return nil
	}
	f, err := s.GetMMFlag(id)
	if err != nil {
		return fmt.Errorf("resolve mm flag %d: %w", id, err)
	}
	if f.Resolution != "" {
		return fmt.Errorf("resolve mm flag %d: already resolved %s", id, f.Resolution)
	}
	return fmt.Errorf("resolve mm flag %d: the flag is %s, and only a raised flag can be resolved", id, f.State)
}
